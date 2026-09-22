package jsonrpc

import (
	"context"
	"testing"

	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/komari-monitor/komari/database/tasks"
	"github.com/komari-monitor/komari/pkg/rpc"
)

func TestLegacyTaskResultCannotSelectAnotherClient(t *testing.T) {
	db := dbcore.GetDBInstance()
	clients := []models.Client{
		{UUID: "legacy-result-owner", Token: "legacy-result-owner-token", Name: "owner"},
		{UUID: "legacy-result-other", Token: "legacy-result-other-token", Name: "other"},
	}
	if err := db.Create(&clients).Error; err != nil {
		t.Fatal(err)
	}
	taskID := "legacy-result-task"
	if err := tasks.CreateTask(taskID, []string{clients[0].UUID, clients[1].UUID}, "true"); err != nil {
		t.Fatal(err)
	}
	ctx := rpc.NewContextWithMeta(context.Background(), &rpc.ContextMeta{ClientUUID: clients[0].UUID})
	_, err := clientTaskResult(ctx, &rpc.JsonRpcRequest{Params: map[string]any{
		"task_id": taskID, "client": clients[1].UUID, "uuid": clients[1].UUID, "result": "done", "exit_code": 7,
	}})
	if err != nil {
		t.Fatal(err)
	}
	own, ownErr := tasks.GetSpecificTaskResult(taskID, clients[0].UUID)
	other, otherErr := tasks.GetSpecificTaskResult(taskID, clients[1].UUID)
	if ownErr != nil || otherErr != nil {
		t.Fatalf("read results: %v %v", ownErr, otherErr)
	}
	if own.Result != "done" || own.ExitCode == nil || *own.ExitCode != 7 {
		t.Fatalf("authenticated result missing: %#v", own)
	}
	if other.Result != "" || other.ExitCode != nil {
		t.Fatalf("another client's result was overwritten: %#v", other)
	}
}

func TestLegacyClientRPCRequiresAuthenticatedUUID(t *testing.T) {
	request := &rpc.JsonRpcRequest{Params: map[string]any{"uuid": "forged", "task_id": "task"}}
	if _, err := clientTaskResult(context.Background(), request); err == nil {
		t.Fatal("task result accepted forged body identity")
	}
	if _, err := clientUploadPingResult(context.Background(), request); err == nil {
		t.Fatal("ping result accepted forged body identity")
	}
	if _, err := clientGetPingTasks(context.Background(), request); err == nil {
		t.Fatal("ping tasks accepted forged body identity")
	}
}
