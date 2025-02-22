/*
Copyright 2021 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package workspaces

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	virtualcmd "github.com/kcp-dev/kcp/pkg/virtual/framework/cmd"
	workspacescmd "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cmd"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/e2e/virtual/helpers"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

type testDataType struct {
	user1, user2, user3                                                      framework.User
	workspace1, workspace1Disambiguited, workspace2, workspace2Disambiguited *tenancyv1beta1.Workspace
}

var testData = testDataType{
	user1: framework.User{
		Name:   "user-1",
		UID:    "1111-1111-1111-1111",
		Token:  "user-1-token",
		Groups: []string{"team-1"},
	},
	user2: framework.User{
		Name:   "user-2",
		UID:    "2222-2222-2222-2222",
		Token:  "user-2-token",
		Groups: []string{"team-2"},
	},
	user3: framework.User{
		Name:   "user-3",
		UID:    "3333-3333-3333-3333",
		Token:  "user-3-token",
		Groups: []string{"team-3"},
	},
	workspace1:              &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace1"}},
	workspace1Disambiguited: &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace1--1"}},
	workspace2:              &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace2"}},
	workspace2Disambiguited: &tenancyv1beta1.Workspace{ObjectMeta: metav1.ObjectMeta{Name: "workspace2--1"}},
}

func TestWorkspacesVirtualWorkspaces(t *testing.T) {
	t.Parallel()

	type runningServer struct {
		framework.RunningServer
		orgKubeClient                  kubernetes.Interface
		orgKcpClient, rootKcpClient    clientset.Interface
		virtualWorkspaceClientContexts []helpers.VirtualWorkspaceClientContext
		virtualWorkspaceClients        []clientset.Interface
		virtualWorkspaceExpectations   []framework.RegisterWorkspaceListExpectation
	}
	var testCases = []struct {
		name                           string
		virtualWorkspaceClientContexts func(orgName string) []helpers.VirtualWorkspaceClientContext
		work                           func(ctx context.Context, t *testing.T, server runningServer)
	}{
		{
			name: "create a workspace in personal virtual workspace and have only its owner list it",
			virtualWorkspaceClientContexts: func(orgName string) []helpers.VirtualWorkspaceClientContext {
				return []helpers.VirtualWorkspaceClientContext{
					{
						User:   testData.user1,
						Prefix: "/" + orgName + "/personal",
					},
					{
						User:   testData.user2,
						Prefix: "/" + orgName + "/personal",
					},
				}
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				vwUser1Client := server.virtualWorkspaceClients[0]
				vwUser2Client := server.virtualWorkspaceClients[1]

				t.Logf("Create Workspace workspace1 in the virtual workspace")
				workspace1, err := vwUser1Client.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace1")

				t.Logf("Verify that the Workspace results in a ClusterWorkspace of the same name in the org workspace")
				_, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
				require.NoError(t, err, "expected to see workspace1 as ClusterWorkspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				})

				t.Logf("Create Workspace workspace2 in the virtual workspace")
				workspace2, err := vwUser2Client.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace2.DeepCopy(), metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace2")

				t.Logf("Verify that the Workspace results in a ClusterWorkspace of the same name in the org workspace")
				_, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace2.Name, metav1.GetOptions{})
				require.NoError(t, err, "expected to see workspace2 as ClusterWorkspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace2.Name, metav1.GetOptions{})
				})

				err = server.virtualWorkspaceExpectations[0](func(w *tenancyv1beta1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace1.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace1.Name, w)
					}
					return nil
				})
				require.NoError(t, err, "did not see the workspace created in personal virtual workspace")
				err = server.virtualWorkspaceExpectations[1](func(w *tenancyv1beta1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace2.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace2.Name, w)
					}
					return nil
				})
				require.NoError(t, err, "did not see workspace2 created in personal virtual workspace")
			},
		},
		{
			name: "create a workspace in personal virtual workspace for an organization and don't see it in another organization",
			virtualWorkspaceClientContexts: func(orgName string) []helpers.VirtualWorkspaceClientContext {
				return []helpers.VirtualWorkspaceClientContext{
					{
						User:   testData.user1,
						Prefix: "/" + orgName + "/personal",
					},
					{
						User:   testData.user1,
						Prefix: "/root:default/personal",
					},
				}
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				testOrgClient := server.virtualWorkspaceClients[0]
				defaultOrgClient := server.virtualWorkspaceClients[1]

				t.Logf("Create Workspace workspace1 in test org")
				workspace1, err := testOrgClient.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace1")

				t.Logf("Verify that the Workspace results in a ClusterWorkspace of the same name in the org workspace")
				_, err = server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
				require.NoError(t, err, "expected to see workspace1 as ClusterWorkspace")
				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				})

				t.Logf("Create Workspace workspace2 in the virtual workspace")
				workspace2, err := defaultOrgClient.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace2.DeepCopy(), metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace2")

				err = server.virtualWorkspaceExpectations[0](func(w *tenancyv1beta1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace1.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace1.Name, w)
					}
					return nil
				})
				require.NoError(t, err, "did not see the workspace1 created in test org")
				err = server.virtualWorkspaceExpectations[1](func(w *tenancyv1beta1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace2.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace2.Name, w)
					}
					return nil
				})
				require.NoError(t, err, "did not see workspace2 created in test org")
			},
		},
		{
			name: "create a workspace in personal virtual workspace and retrieve its kubeconfig",
			virtualWorkspaceClientContexts: func(orgName string) []helpers.VirtualWorkspaceClientContext {
				return []helpers.VirtualWorkspaceClientContext{
					{
						User:   testData.user1,
						Prefix: "/" + orgName + "/personal",
					},
				}
			},
			work: func(ctx context.Context, t *testing.T, server runningServer) {
				vwUser1Client := server.virtualWorkspaceClients[0]
				_, err := server.orgKubeClient.CoreV1().Namespaces().Create(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}, metav1.CreateOptions{})
				require.NoError(t, err, "failed to create namespace")

				kcpServerKubeconfig, err := server.RunningServer.RawConfig()
				require.NoError(t, err, "failed to get KCP Kubeconfig")

				workspace1, err := vwUser1Client.TenancyV1beta1().Workspaces().Create(ctx, testData.workspace1.DeepCopy(), metav1.CreateOptions{})
				require.NoError(t, err, "failed to create workspace1")

				server.Artifact(t, func() (runtime.Object, error) {
					return server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, testData.workspace1.Name, metav1.GetOptions{})
				})

				workspaceURL := ""
				var lastErr error
				err = wait.PollImmediate(time.Millisecond*100, wait.ForeverTestTimeout, func() (done bool, err error) {
					defer func() {
						lastErr = err
					}()

					cw, err := server.orgKcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, workspace1.Name, metav1.GetOptions{})
					if err != nil {
						if apierrors.IsNotFound(err) {
							return false, nil
						}
						return false, err
					} else if !conditions.IsTrue(cw, tenancyv1alpha1.WorkspaceShardValid) {
						return false, fmt.Errorf("ClusterWorkspace %s is not valid: %s", cw.Name, conditions.GetMessage(cw, tenancyv1alpha1.WorkspaceShardValid))
					}
					workspaceURL = cw.Status.BaseURL
					return true, nil
				})
				require.NoError(t, err, "did not see the workspace created and valid in KCP: %v", lastErr)

				err = server.virtualWorkspaceExpectations[0](func(w *tenancyv1beta1.WorkspaceList) error {
					if len(w.Items) != 1 || w.Items[0].Name != workspace1.Name {
						return fmt.Errorf("expected only one workspace (%s), got %#v", workspace1.Name, w)
					}
					return nil
				})
				require.NoError(t, err, "did not see the workspace created in personal virtual workspace")

				req := vwUser1Client.TenancyV1beta1().RESTClient().Get().Resource("workspaces").Name(workspace1.Name).SubResource("kubeconfig").Do(ctx)
				require.Nil(t, req.Error(), "error retrieving the kubeconfig for workspace %s: %v", workspace1.Name, err)

				kcpConfigCurrentContextName := kcpServerKubeconfig.CurrentContext
				kcpConfigCurrentContext := kcpServerKubeconfig.Contexts[kcpConfigCurrentContextName]
				require.NotNil(t, kcpConfigCurrentContext, "kcp Kubeconfig is invalid")

				kcpConfigCurrentCluster := kcpServerKubeconfig.Clusters[kcpConfigCurrentContext.Cluster]
				require.NotNil(t, kcpConfigCurrentCluster, "kcp Kubeconfig is invalid")

				expectedKubeconfigCluster := kcpConfigCurrentCluster.DeepCopy()
				expectedKubeconfigCluster.Server = workspaceURL
				expectedKubeconfig := &clientcmdapi.Config{
					CurrentContext: "personal/" + workspace1.Name,
					Contexts: map[string]*clientcmdapi.Context{
						"personal/" + workspace1.Name: {
							Cluster: "personal/" + workspace1.Name,
						},
					},
					Clusters: map[string]*clientcmdapi.Cluster{
						"personal/" + workspace1.Name: expectedKubeconfigCluster,
					},
				}
				expectedKubeconfigContent, err := clientcmd.Write(*expectedKubeconfig)
				require.NoError(t, err, "error writing the content of the expected kubeconfig for workspace %s", workspace1.Name)

				workspaceKubeconfigContent, err := req.Raw()
				require.NoError(t, err, "error retrieving the content of the kubeconfig for workspace %s", workspace1.Name)

				require.YAMLEq(t, string(expectedKubeconfigContent), string(workspaceKubeconfigContent))
			},
		},
	}

	const serverName = "main"

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var users []framework.User
			for _, vwClientContexts := range testCase.virtualWorkspaceClientContexts("") {
				users = append(users, vwClientContexts.User)
			}
			usersKCPArgs, err := framework.Users(users).ArgsForKCP(t)
			require.NoError(t, err)

			// TODO(marun) Can fixture be shared for this test?
			f := framework.NewKcpFixture(t,
				framework.KcpConfig{
					Name: serverName,
					Args: append([]string{
						"--run-controllers=false",
						"--unsupported-run-individual-controllers=workspace-scheduler",
					}, usersKCPArgs...),
				},
			)

			require.Equal(t, 1, len(f.Servers), "incorrect number of servers")
			server := f.Servers[serverName]

			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				withDeadline, cancel := context.WithDeadline(ctx, deadline)
				t.Cleanup(cancel)
				ctx = withDeadline
			}

			orgClusterName := framework.NewOrganizationFixture(t, server)

			clientContexts := testCase.virtualWorkspaceClientContexts(orgClusterName)

			vw := helpers.VirtualWorkspace{
				BuildSubCommandOptions: func(kcpServer framework.RunningServer) virtualcmd.SubCommandOptions {
					kcpAdminConfig, _ := kcpServer.RawConfig()
					var baseCluster = *kcpAdminConfig.Clusters["system:admin"] // shallow copy
					virtualWorkspaceKubeConfig := clientcmdapi.Config{
						Clusters: map[string]*clientcmdapi.Cluster{
							"shard": &baseCluster,
						},
						Contexts: map[string]*clientcmdapi.Context{
							"shard": {
								Cluster:  "shard",
								AuthInfo: "virtualworkspace",
							},
						},
						AuthInfos: map[string]*clientcmdapi.AuthInfo{
							"virtualworkspace": kcpAdminConfig.AuthInfos["admin"],
						},
						CurrentContext: "shard",
					}

					// write kubeconfig to disk, next to kcp kubeconfig
					cfgPath := filepath.Join(filepath.Dir(kcpServer.KubeconfigPath()), "virtualworkspace.kubeconfig")
					err = clientcmd.WriteToFile(virtualWorkspaceKubeConfig, cfgPath)
					require.NoError(t, err)

					return &workspacescmd.WorkspacesSubCommandOptions{
						KubeconfigFile: cfgPath,
						RootPathPrefix: "/",
					}
				},
				ClientContexts: clientContexts,
			}

			vwConfigs, err := vw.Setup(t, ctx, server)
			require.NoError(t, err)

			virtualWorkspaceClients := []clientset.Interface{}
			virtualWorkspaceExpectations := []framework.RegisterWorkspaceListExpectation{}
			for _, vwConfig := range vwConfigs {
				vwClients, err := clientset.NewForConfig(vwConfig)
				require.NoError(t, err, "failed to construct client for server")

				virtualWorkspaceClients = append(virtualWorkspaceClients, vwClients)

				expecter, err := framework.ExpectWorkspaceListPolling(ctx, t, vwClients)
				require.NoError(t, err, "failed to start expecter")

				virtualWorkspaceExpectations = append(virtualWorkspaceExpectations, expecter)
			}

			kcpCfg, err := server.DefaultConfig()
			require.NoError(t, err)

			kubeClusterClient, err := kubernetes.NewClusterForConfig(kcpCfg)
			require.NoError(t, err, "failed to construct client for server")

			kcpClusterClient, err := clientset.NewClusterForConfig(kcpCfg)
			require.NoError(t, err, "failed to construct client for server")

			testCase.work(ctx, t, runningServer{
				RunningServer:                  server,
				orgKubeClient:                  kubeClusterClient.Cluster(orgClusterName),
				orgKcpClient:                   kcpClusterClient.Cluster(orgClusterName),
				rootKcpClient:                  kcpClusterClient.Cluster(helper.RootCluster),
				virtualWorkspaceClientContexts: clientContexts,
				virtualWorkspaceClients:        virtualWorkspaceClients,
				virtualWorkspaceExpectations:   virtualWorkspaceExpectations,
			})
		})
	}
}
