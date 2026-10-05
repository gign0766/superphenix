package vmSnapshot

import (
	"context"
	"errors"
	"testing"

	"github.com/super-phenix/superphenix/internal/superphenix-controller/pkg/config"

	spxId "github.com/super-phenix/superphenix/pkg/superphenix-id"

	"go.uber.org/mock/gomock"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8smetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	k8stesting "k8s.io/client-go/testing"
	virtv1 "kubevirt.io/api/core/v1"
	"kubevirt.io/api/snapshot/v1beta1"
	"kubevirt.io/client-go/kubecli"
	snapshotfake "kubevirt.io/client-go/kubevirt/typed/snapshot/v1beta1/fake"
)

const (
	restoreTestOrgId     = "6f1c1a4e-2d1b-4c55-9a43-3f4f1e2b7a10"
	restoreTestProjectId = "0b8a7c55-8f1e-4d3a-9e57-2c6d4b1a9f21"
	restoreTestSnapshot  = "spx-snapshot"
	restoreTestContent   = "spx-snapshot-content"
)

func restoreTestEid(t *testing.T, localId string) string {
	t.Helper()
	m := spxId.Metadata{}
	if err := m.GenerateMetadata(restoreTestProjectId, restoreTestOrgId, localId); err != nil {
		t.Fatalf("failed to generate metadata: %v", err)
	}
	return m.GetResourceEffectiveID()
}

func setupFakeRestoreClient(t *testing.T, objects ...runtime.Object) (*k8stesting.Fake, func()) {
	t.Helper()
	ctrl := gomock.NewController(t)
	mockClient := kubecli.NewMockKubevirtClient(ctrl)

	scheme := runtime.NewScheme()
	_ = v1beta1.AddToScheme(scheme)
	tracker := k8stesting.NewObjectTracker(scheme, serializer.NewCodecFactory(scheme).UniversalDecoder())
	for _, obj := range objects {
		if err := tracker.Add(obj); err != nil {
			t.Fatalf("failed to add object: %v", err)
		}
	}
	fake := &k8stesting.Fake{}
	fake.AddReactor("*", "*", k8stesting.ObjectReaction(tracker))
	fakeSnapshot := &snapshotfake.FakeSnapshotV1beta1{Fake: fake}

	mockClient.EXPECT().VirtualMachineSnapshot(gomock.Any()).DoAndReturn(func(ns string) any {
		return fakeSnapshot.VirtualMachineSnapshots(ns)
	}).AnyTimes()
	mockClient.EXPECT().VirtualMachineSnapshotContent(gomock.Any()).DoAndReturn(func(ns string) any {
		return fakeSnapshot.VirtualMachineSnapshotContents(ns)
	}).AnyTimes()
	mockClient.EXPECT().VirtualMachineRestore(gomock.Any()).DoAndReturn(func(ns string) any {
		return fakeSnapshot.VirtualMachineRestores(ns)
	}).AnyTimes()

	old := config.VirtClient
	config.VirtClient = mockClient
	return fake, func() { config.VirtClient = old }
}

func TestRestoreVmSnapshot(t *testing.T) {
	namespace := spxId.ToSPXID(restoreTestProjectId)
	apiLocalId := "3d0f4a1e-6b7c-4e8f-9a0b-1c2d3e4f5a6b"
	gitopsLocalId := "my-gitops-vm"
	contentName := restoreTestContent

	newSnapshot := func(sourceEid string, withContent bool) *v1beta1.VirtualMachineSnapshot {
		s := &v1beta1.VirtualMachineSnapshot{
			ObjectMeta: k8smetav1.ObjectMeta{
				Name:      restoreTestSnapshot,
				Namespace: namespace,
				Labels:    map[string]string{spxId.SpxLabelProjectID: namespace},
			},
		}
		s.Spec.Source.Kind = virtv1.VirtualMachineGroupVersionKind.Kind
		s.Spec.Source.Name = sourceEid
		if withContent {
			s.Status = &v1beta1.VirtualMachineSnapshotStatus{VirtualMachineSnapshotContentName: &contentName}
		}
		return s
	}
	newContent := func(gitops string) *v1beta1.VirtualMachineSnapshotContent {
		c := &v1beta1.VirtualMachineSnapshotContent{
			ObjectMeta: k8smetav1.ObjectMeta{Name: restoreTestContent, Namespace: namespace},
		}
		c.Spec.Source.VirtualMachine = &v1beta1.VirtualMachine{
			ObjectMeta: k8smetav1.ObjectMeta{Labels: map[string]string{spxId.SpxLabelGitops: gitops}},
		}
		return c
	}

	tests := []struct {
		name        string
		objects     []runtime.Object
		localId     string
		wantErr     func(error) bool
		wantTarget  RestoreTarget
		wantRestore bool
	}{
		{
			name:        "restore API instance with matching local ID",
			objects:     []runtime.Object{newSnapshot(restoreTestEid(t, apiLocalId), true), newContent("false")},
			localId:     apiLocalId,
			wantTarget:  RestoreTarget{LocalId: apiLocalId, EffectiveId: restoreTestEid(t, apiLocalId), Gitops: "false"},
			wantRestore: true,
		},
		{
			name:        "restore GitOps instance with name local ID",
			objects:     []runtime.Object{newSnapshot(restoreTestEid(t, gitopsLocalId), true), newContent("true")},
			localId:     gitopsLocalId,
			wantTarget:  RestoreTarget{LocalId: gitopsLocalId, EffectiveId: restoreTestEid(t, gitopsLocalId), Gitops: "true"},
			wantRestore: true,
		},
		{
			name:        "restore without snapshot content",
			objects:     []runtime.Object{newSnapshot(restoreTestEid(t, apiLocalId), false)},
			localId:     apiLocalId,
			wantTarget:  RestoreTarget{LocalId: apiLocalId, EffectiveId: restoreTestEid(t, apiLocalId)},
			wantRestore: true,
		},
		{
			name:    "local ID of another instance is rejected",
			objects: []runtime.Object{newSnapshot(restoreTestEid(t, apiLocalId), true), newContent("false")},
			localId: "11111111-2222-4333-8444-555555555555",
			wantErr: func(err error) bool { return errors.Is(err, ErrRestoreTargetMismatch) },
		},
		{
			name:    "empty local ID is rejected",
			objects: []runtime.Object{newSnapshot(restoreTestEid(t, apiLocalId), true), newContent("false")},
			localId: "",
			wantErr: func(err error) bool { return errors.Is(err, ErrRestoreTargetMismatch) },
		},
		{
			name:    "invalid local ID is rejected",
			objects: []runtime.Object{newSnapshot(restoreTestEid(t, apiLocalId), true), newContent("false")},
			localId: "Not_A_Valid_ID",
			wantErr: func(err error) bool { return errors.Is(err, ErrRestoreTargetMismatch) },
		},
		{
			name:    "snapshot not found",
			objects: []runtime.Object{},
			localId: apiLocalId,
			wantErr: apierrors.IsNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake, cleanup := setupFakeRestoreClient(t, tt.objects...)
			defer cleanup()

			target, err := RestoreVmSnapshot(context.Background(), restoreTestOrgId, restoreTestProjectId, restoreTestSnapshot, tt.localId)

			if tt.wantErr != nil {
				if err == nil || !tt.wantErr(err) {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			} else if target != tt.wantTarget {
				t.Errorf("target = %+v, want %+v", target, tt.wantTarget)
			}

			restores, err := (&snapshotfake.FakeSnapshotV1beta1{Fake: fake}).VirtualMachineRestores(namespace).List(context.Background(), k8smetav1.ListOptions{})
			if err != nil {
				t.Fatalf("failed to list restores: %v", err)
			}
			if gotRestore := len(restores.Items) > 0; gotRestore != tt.wantRestore {
				t.Errorf("restore created = %t, want %t", gotRestore, tt.wantRestore)
			}
		})
	}
}
