// tests/a2a_client_test.go
package tests

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	a2aClient "github.com/gate4ai/mcp/gateway/clients/a2aClient"
	"github.com/gate4ai/mcp/shared"
	a2aSchema "github.com/gate4ai/mcp/shared/a2a/2025-draft/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"
)

// Helper function to create a new A2A client for tests
func newTestA2AClient(t *testing.T) *a2aClient.Client {
	t.Helper()
	logger := zaptest.NewLogger(t)
	client, err := a2aClient.New(
		EXAMPLE_A2A_SERVER_URL,
		a2aClient.WithLogger(logger),
		a2aClient.DoNotTrustAgentInfoURL())
	require.NoError(t, err, "Failed to create A2A client")
	return client
}

// Test A2A Discovery (Fetching Agent Card)
func TestA2ADiscovery(t *testing.T) {
	client := newTestA2AClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100000*time.Second)
	defer cancel()

	agentInfo, err := client.FetchAgentInfo(ctx)
	require.NoError(t, err, "FetchAgentInfo failed")
	require.NotNil(t, agentInfo, "AgentInfo should not be nil")

	t.Logf("Fetched AgentCard: Name=%s, Version=%s, URL=%s", agentInfo.Name, agentInfo.Version, agentInfo.URL)

	// Assertions based on the expected AgentCard from the coder agent example
	assert.Equal(t, "Coder Agent", agentInfo.Name) // Matches name in main.go
	assert.Equal(t, "0.0.1", agentInfo.Version)
	assert.True(t, agentInfo.Capabilities.Streaming, "Expected streaming capability to be true")
	assert.False(t, agentInfo.Capabilities.PushNotifications)
	assert.Contains(t, agentInfo.DefaultInputModes, "text")
	assert.Contains(t, agentInfo.DefaultOutputModes, "text")
	assert.Contains(t, agentInfo.DefaultOutputModes, "file")
	require.NotEmpty(t, agentInfo.Skills, "Expected at least one skill")
	assert.Equal(t, "code_generation", agentInfo.Skills[0].ID)
	assert.Equal(t, "Code Generation", agentInfo.Skills[0].Name)
}

// Test tasks/send
func TestA2ATaskSend(t *testing.T) {
	client := newTestA2AClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // Longer timeout for task execution
	defer cancel()

	taskID := fmt.Sprintf("task-send-%d", time.Now().UnixNano())
	sessionID := fmt.Sprintf("session-%d", time.Now().UnixNano())
	prompt := "Generate a simple hello world in Python"

	params := a2aSchema.TaskSendParams{
		ID:        taskID,
		SessionID: &sessionID,
		Message: a2aSchema.Message{
			Role:  "user",
			Parts: []a2aSchema.Part{{Type: shared.PointerTo("text"), Text: shared.PointerTo(prompt)}},
		},
	}

	task, err := client.SendTask(ctx, params)
	require.NoError(t, err, "SendTask failed")
	require.NotNil(t, task, "Task result should not be nil")

	t.Logf("SendTask result: ID=%s, Status=%s", task.ID, task.Status.State)

	// Verify the final state
	require.Equal(t, a2aSchema.TaskStateCompleted, task.Status.State, "Task should be completed")
	require.NotNil(t, task.Status.Message, "Final status message should not be nil")
	require.NotEmpty(t, task.Status.Message.Parts, "Final status message should have parts")
	// Check for artifact
	require.NotEmpty(t, task.Artifacts, "Expected artifacts in the final task state")
	assert.GreaterOrEqual(t, len(task.Artifacts), 1, "Expected at least one artifact")

	// Inspect the first artifact (assuming it's the code file)
	artifact := task.Artifacts[0]
	assert.NotNil(t, artifact.Name, "Artifact name should not be nil")

	textPart := artifact.Parts[0]
	require.Equal(t, "text", *textPart.Type, "Artifact part should be TextPart")
	require.NoError(t, err, "Artifact part is TextPart")
	t.Logf("Artifact content (TextPart): %s", *textPart.Text)
	assert.Contains(t, *textPart.Text, "console.log", "Artifact content should contain console.log")
}

// Test tasks/get
func TestA2ATaskGet(t *testing.T) {
	client := newTestA2AClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	taskID := fmt.Sprintf("task-get-%d", time.Now().UnixNano())
	sessionID := fmt.Sprintf("session-%d", time.Now().UnixNano())
	prompt := "Generate a short bash script to list files"

	// Send the task first (don't wait for full completion if it's slow)
	go func() {
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer sendCancel()
		_, _ = client.SendTask(sendCtx, a2aSchema.TaskSendParams{
			ID:        taskID,
			SessionID: &sessionID,
			Message: a2aSchema.Message{
				Role:  "user",
				Parts: []a2aSchema.Part{{Type: shared.PointerTo("text"), Text: shared.PointerTo(prompt)}},
			},
		})
	}()

	// Wait a very short time for the task to likely be submitted/working
	time.Sleep(500 * time.Millisecond)

	// Get the task status
	getParams := a2aSchema.TaskQueryParams{ID: taskID}
	task, err := client.GetTask(ctx, getParams)
	require.NoError(t, err, "GetTask failed")
	require.NotNil(t, task, "GetTask result should not be nil")

	t.Logf("GetTask result: ID=%s, Status=%s", task.ID, task.Status.State)

	// Status could be submitted, working, or even completed if fast
	require.Contains(t,
		[]a2aSchema.TaskState{
			a2aSchema.TaskStateSubmitted,
			a2aSchema.TaskStateWorking,
			a2aSchema.TaskStateCompleted,
		},
		task.Status.State, "Task status should be submitted, working, or completed")

	// Optional: Wait longer and get again to check for completion
	time.Sleep(5 * time.Second)
	task, err = client.GetTask(ctx, getParams)
	require.NoError(t, err, "Second GetTask failed")
	require.NotNil(t, task, "Second GetTask result should not be nil")
	t.Logf("Second GetTask result: ID=%s, Status=%s", task.ID, task.Status.State)
	require.Equal(t, a2aSchema.TaskStateCompleted, task.Status.State, "Task should be completed after wait")
}

// Test tasks/cancel
func TestA2ATaskCancel(t *testing.T) {
	client := newTestA2AClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	taskID := fmt.Sprintf("task-cancel-%d", time.Now().UnixNano())
	sessionID := fmt.Sprintf("session-%d", time.Now().UnixNano())
	// Use a prompt that might take a moment, though the example server is fast
	prompt := "Write a slightly longer Go program with comments"

	// Send the task in the background
	go func() {
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer sendCancel()
		_, _ = client.SendTask(sendCtx, a2aSchema.TaskSendParams{
			ID:        taskID,
			SessionID: &sessionID,
			Message: a2aSchema.Message{
				Role:  "user",
				Parts: []a2aSchema.Part{{Type: shared.PointerTo("text"), Text: shared.PointerTo(prompt)}},
			},
		})
	}()

	// Wait briefly for the task to start
	time.Sleep(200 * time.Millisecond)

	// Attempt to cancel the task
	cancelParams := a2aSchema.TaskIdParams{ID: taskID}
	canceledTask, err := client.CancelTask(ctx, cancelParams)

	// Check the result - the example JS coder agent might not support cancellation
	// and might complete before cancel hits, or ignore cancel.
	if err != nil {
		// Check if the error is specifically TaskNotCancelableError
		var jsonRpcErr *shared.JSONRPCError
		if errors.As(err, &jsonRpcErr) && jsonRpcErr.Code == a2aSchema.ErrorCodeTaskNotCancelable {
			t.Logf("CancelTask failed as expected (task not cancelable): %v", err)
			// This might be okay if the server doesn't support cancel or task finished too fast.
			// Let's get the final status to confirm.
			finalTask, getErr := client.GetTask(ctx, a2aSchema.TaskQueryParams{ID: taskID})
			require.NoError(t, getErr, "GetTask failed after cancel attempt")
			require.NotNil(t, finalTask)
			t.Logf("Final task status after cancel attempt: %s", finalTask.Status.State)
			assert.Contains(t, []a2aSchema.TaskState{a2aSchema.TaskStateCompleted, a2aSchema.TaskStateCanceled}, finalTask.Status.State)
			return // End test here if TaskNotCancelable is returned
		}
		// If it's another error, fail the test
		require.NoError(t, err, "CancelTask failed unexpectedly")
	}

	// If CancelTask succeeded without error (or returned TaskNotCancelable):
	require.NotNil(t, canceledTask, "CancelTask result should not be nil if no error occurred")
	t.Logf("CancelTask response status: %s", canceledTask.Status.State)
	// The status *might* be CANCELED, or it might still be WORKING/COMPLETED if cancel was too late/ignored.
	assert.Contains(t,
		[]a2aSchema.TaskState{
			a2aSchema.TaskStateCanceled,
			a2aSchema.TaskStateCompleted, // Allow completed if cancellation was slow/ignored
			a2aSchema.TaskStateWorking,   // Allow working if cancellation is async and hasn't processed fully
		},
		canceledTask.Status.State, "Task status after cancel should be canceled, completed, or working")

	// Optional: Get status again after a short delay to see if it became canceled
	time.Sleep(500 * time.Millisecond)
	finalTask, getErr := client.GetTask(ctx, a2aSchema.TaskQueryParams{ID: taskID})
	require.NoError(t, getErr, "GetTask failed after cancel")
	require.NotNil(t, finalTask)
	t.Logf("Final task status after cancel: %s", finalTask.Status.State)
	assert.Contains(t, []a2aSchema.TaskState{a2aSchema.TaskStateCanceled, a2aSchema.TaskStateCompleted}, finalTask.Status.State)
}

// Test tasks/sendSubscribe
func TestA2ATaskSendSubscribe(t *testing.T) {
	client := newTestA2AClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // Timeout for the whole test
	defer cancel()

	taskID := fmt.Sprintf("task-subscribe-%d", time.Now().UnixNano())
	sessionID := fmt.Sprintf("session-%d", time.Now().UnixNano())
	prompt := "Create a small html file and a css file for styling"

	params := a2aSchema.TaskSendParams{
		ID:        taskID,
		SessionID: &sessionID,
		Message: a2aSchema.Message{
			Role:  "user",
			Parts: []a2aSchema.Part{{Type: shared.PointerTo("text"), Text: &prompt}},
		},
	}

	eventChan, err := client.SendTaskSubscribe(ctx, params)
	require.NoError(t, err, "SendTaskSubscribe failed to initiate")
	require.NotNil(t, eventChan, "Event channel should not be nil")

	receivedWorking := false
	receivedArtifacts := 0
	receivedFinalCompleted := false
	timeout := time.After(15 * time.Second) // Timeout for receiving events

	t.Log("Waiting for SSE events...")
	for {
		select {
		case event, ok := <-eventChan:
			if !ok {
				t.Log("Event channel closed")
				goto EndLoop // Exit outer loop if channel closed
			}

			require.NoError(t, event.Error, "Received error event from stream")

			if event.Status != nil {
				t.Logf("Received Status Event: State=%s, Final=%t, Message=%v", event.Status.Status.State, event.Final, event.Status.Status.Message)
				if event.Status.Status.State == a2aSchema.TaskStateWorking {
					receivedWorking = true
				}
				if event.Status.Status.State == a2aSchema.TaskStateCompleted && event.Final {
					receivedFinalCompleted = true
					goto EndLoop // Exit loop once final completed status received
				}
				if event.Final && event.Status.Status.State != a2aSchema.TaskStateCompleted {
					t.Fatalf("Received final event but state was not completed: %s", event.Status.Status.State)
				}
			} else if event.Artifact != nil {
				t.Logf("Received Artifact Event: Index=%d, Name=%s, Parts=%d", event.Artifact.Artifact.Index, *event.Artifact.Artifact.Name, len(event.Artifact.Artifact.Parts))
				receivedArtifacts++
				// Optionally inspect artifact content
				if len(event.Artifact.Artifact.Parts) > 0 {
					t.Logf("  Artifact Part 0 Type: %s", *event.Artifact.Artifact.Parts[0].Type)
				}
			} else {
				t.Errorf("Received A2AStreamEvent with neither Status nor Artifact")
			}

		case <-timeout:
			t.Fatal("Timeout waiting for SSE events")

		case <-ctx.Done():
			t.Fatal("Test context cancelled")
		}
	}

EndLoop:
	// Assertions after the loop
	assert.True(t, receivedWorking, "Did not receive 'working' status update")
	// The JS coder agent generates multiple files (3 in the example)
	assert.GreaterOrEqual(t, receivedArtifacts, 1, "Expected at least one artifact update")
	assert.True(t, receivedFinalCompleted, "Did not receive final 'completed' status update")
	t.Logf("Finished processing stream. Received %d artifacts.", receivedArtifacts)
}
