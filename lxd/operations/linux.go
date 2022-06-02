//go:build linux && cgo && !agent

package operations

import (
	"context"
	"fmt"

	"github.com/lxc/lxd/lxd/db"
	"github.com/lxc/lxd/lxd/db/cluster"
)

func registerDBOperation(op *Operation, opType db.OperationType) error {
	if op.state == nil {
		return nil
	}

	err := op.state.DB.Cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		opInfo := db.Operation{
			UUID:   op.id,
			Type:   opType,
			NodeID: tx.GetNodeID(),
		}

		if op.projectName != "" {
			projectID, err := cluster.GetProjectID(context.Background(), tx.Tx(), op.projectName)
			if err != nil {
				return fmt.Errorf("Fetch project ID: %w", err)
			}
			opInfo.ProjectID = &projectID
		}

		_, err := tx.CreateOrReplaceOperation(opInfo)
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to add %q Operation %s to database: %w", opType.Description(), op.id, err)
	}

	return nil
}

func removeDBOperation(op *Operation) error {
	if op.state == nil {
		return nil
	}

	err := op.state.DB.Cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		return tx.DeleteOperation(op.id)
	})

	return err
}

func getServerName(op *Operation) (string, error) {
	if op.state == nil {
		return "", nil
	}

	var serverName string
	var err error
	err = op.state.DB.Cluster.Transaction(context.TODO(), func(ctx context.Context, tx *db.ClusterTx) error {
		serverName, err = tx.GetLocalNodeName()
		return err
	})
	if err != nil {
		return "", err
	}

	return serverName, nil
}

func (op *Operation) sendEvent(eventMessage any) {
	if op.events == nil {
		return
	}

	_ = op.events.Send(op.projectName, "operation", eventMessage)
}
