package vmSnapshot

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/super-phenix/superphenix/internal/superphenix-controller/pkg/config"
	logger "github.com/super-phenix/superphenix/pkg/utils/log"

	spxId "github.com/super-phenix/superphenix/pkg/superphenix-id"

	v1 "k8s.io/api/core/v1"
	k8smetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"kubevirt.io/api/snapshot/v1beta1"
)

// ErrRestoreTargetMismatch is returned when the provided local ID does not identify the VM targeted by the snapshot
var ErrRestoreTargetMismatch = errors.New("local ID does not match the snapshot source instance")

// RestoreTarget identifies the instance restored by a snapshot
type RestoreTarget struct {
	LocalId     string `json:"localId"`
	EffectiveId string `json:"effectiveId"`
	Gitops      string `json:"gitops"`
}

// RestoreVmSnapshot restores the snapshot `name` onto its source VM.
// localId must be the local ID of the snapshot source VM, otherwise ErrRestoreTargetMismatch is returned and nothing is restored.
func RestoreVmSnapshot(ctx context.Context, orgId, projectId, name, localId string) (RestoreTarget, error) {
	log := logger.GetLogger(ctx)
	namespace := spxId.ToSPXID(projectId)
	snapshot, err := GetVmSnapshot(ctx, namespace, name)
	if err != nil {
		log.Err(err).Msgf("Error getting vmSnapshot %s in namespace %s", name, namespace)
		return RestoreTarget{}, err
	}

	// The VM name is its effective ID, check that the local ID provided identifies the snapshot source VM
	target := spxId.Metadata{}
	if err := target.GenerateMetadata(projectId, orgId, localId); err != nil {
		log.Err(err).Str("localId", localId).Msg("Invalid restore target local ID")
		return RestoreTarget{}, fmt.Errorf("%w: %w", ErrRestoreTargetMismatch, err)
	}
	if snapshot.Spec.Source.Name != target.GetResourceEffectiveID() {
		log.Warn().Str("localId", localId).Str("computedEid", target.GetResourceEffectiveID()).Str("sourceEid", snapshot.Spec.Source.Name).Msg("Restore target does not match snapshot source")
		return RestoreTarget{}, ErrRestoreTargetMismatch
	}

	gitops := ""
	if snapshot.Status.VirtualMachineSnapshotContentName != nil {
		content, err := config.VirtClient.VirtualMachineSnapshotContent(namespace).Get(ctx, *snapshot.Status.VirtualMachineSnapshotContentName, k8smetav1.GetOptions{})
		if err != nil {
			log.Err(err).Str("namespace", namespace).Str("name", *snapshot.Status.VirtualMachineSnapshotContentName).Msg("Error getting vm snapshot content")
			return RestoreTarget{}, err
		}
		if content.Spec.Source.VirtualMachine != nil {
			gitops = content.Spec.Source.VirtualMachine.Labels[spxId.SpxLabelGitops]
		}
	}

	now := strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
	m := spxId.Metadata{}

	if err := m.GenerateMetadata(projectId, orgId, name+"-"+now); err != nil {
		log.Err(err).Msg("Failed to generate metadata")
		return RestoreTarget{}, err
	}

	readinessPolicy := v1beta1.VirtualMachineRestoreStopTarget
	restorePolicy := v1beta1.VolumeRestorePolicyInPlace
	volumeOwnershipPolicy := v1beta1.VolumeOwnershipPolicyNone

	_, err = config.VirtClient.VirtualMachineRestore(namespace).Create(ctx, &v1beta1.VirtualMachineRestore{
		ObjectMeta: k8smetav1.ObjectMeta{
			Name:   m.GetResourceEffectiveID(),
			Labels: m.GetLabels(),
		},
		Spec: v1beta1.VirtualMachineRestoreSpec{
			Target: v1.TypedLocalObjectReference{
				APIGroup: snapshot.Spec.Source.APIGroup,
				Kind:     snapshot.Spec.Source.Kind,
				Name:     snapshot.Spec.Source.Name,
			},
			VirtualMachineSnapshotName: name,
			TargetReadinessPolicy:      &readinessPolicy,
			VolumeRestorePolicy:        &restorePolicy,
			VolumeOwnershipPolicy:      &volumeOwnershipPolicy,
		},
	}, k8smetav1.CreateOptions{})
	if err != nil {
		log.Err(err).Msgf("Error restoring vmSnapshot %s in namespace %s", name, namespace)
		return RestoreTarget{}, err
	}

	return RestoreTarget{
		LocalId:     localId,
		EffectiveId: target.GetResourceEffectiveID(),
		Gitops:      gitops,
	}, nil
}
