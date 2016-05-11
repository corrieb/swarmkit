package orchestrator

import (
	"testing"

	"github.com/docker/swarm-v2/api"
	"github.com/docker/swarm-v2/manager/state"
	"github.com/docker/swarm-v2/manager/state/store"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/context"
)

func TestTaskHistory(t *testing.T) {
	ctx := context.Background()
	s := store.NewMemoryStore(nil)
	assert.NotNil(t, s)

	taskReaper := NewTaskReaper(s, 2)
	orchestrator := New(s)

	watch, cancel := state.Watch(s.WatchQueue() /*state.EventCreateTask{}, state.EventUpdateTask{}*/)
	defer cancel()

	// Create a service with two instances specified before the orchestrator is
	// started. This should result in two tasks when the orchestrator
	// starts up.
	err := s.Update(func(tx store.Tx) error {
		j1 := &api.Service{
			ID: "id1",
			Spec: api.ServiceSpec{
				Annotations: api.Annotations{
					Name: "name1",
				},
				RuntimeSpec: &api.ServiceSpec_Container{},
				Instances:   2,
				Mode:        api.ServiceModeRunning,
			},
		}
		assert.NoError(t, store.CreateService(tx, j1))
		return nil
	})
	assert.NoError(t, err)

	// Start the orchestrator.
	go func() {
		assert.NoError(t, orchestrator.Run(ctx))
	}()
	go taskReaper.Run()

	observedTask1 := watchTaskCreate(t, watch)
	assert.Equal(t, observedTask1.Status.State, api.TaskStateNew)
	assert.Equal(t, observedTask1.Annotations.Name, "name1")

	observedTask2 := watchTaskCreate(t, watch)
	assert.Equal(t, observedTask2.Status.State, api.TaskStateNew)
	assert.Equal(t, observedTask2.Annotations.Name, "name1")

	// Fail both tasks. They should both get restarted.
	updatedTask1 := observedTask1.Copy()
	updatedTask1.Status.State = api.TaskStateDead
	updatedTask1.Status.TerminalState = api.TaskStateFailed
	updatedTask1.Annotations = api.Annotations{Name: "original"}
	updatedTask2 := observedTask2.Copy()
	updatedTask2.Status.State = api.TaskStateDead
	updatedTask2.Status.TerminalState = api.TaskStateFailed
	updatedTask2.Annotations = api.Annotations{Name: "original"}
	err = s.Update(func(tx store.Tx) error {
		assert.NoError(t, store.UpdateTask(tx, updatedTask1))
		assert.NoError(t, store.UpdateTask(tx, updatedTask2))
		return nil
	})

	expectCommit(t, watch)
	expectTaskUpdate(t, watch)
	expectTaskUpdate(t, watch)
	expectCommit(t, watch)

	expectTaskUpdate(t, watch)
	observedTask3 := watchTaskCreate(t, watch)
	assert.Equal(t, observedTask3.Status.State, api.TaskStateNew)
	assert.Equal(t, observedTask3.Annotations.Name, "name1")

	expectTaskUpdate(t, watch)
	observedTask4 := watchTaskCreate(t, watch)
	assert.Equal(t, observedTask4.Status.State, api.TaskStateNew)
	assert.Equal(t, observedTask4.Annotations.Name, "name1")

	// Fail these replacement tasks. Since TaskHistory is set to 2, this
	// should cause the oldest tasks for each instance to get deleted.
	updatedTask3 := observedTask3.Copy()
	updatedTask3.Status.State = api.TaskStateDead
	updatedTask3.Status.TerminalState = api.TaskStateFailed
	updatedTask4 := observedTask4.Copy()
	updatedTask4.Status.State = api.TaskStateDead
	updatedTask4.Status.TerminalState = api.TaskStateFailed
	err = s.Update(func(tx store.Tx) error {
		assert.NoError(t, store.UpdateTask(tx, updatedTask3))
		assert.NoError(t, store.UpdateTask(tx, updatedTask4))
		return nil
	})

	deletedTask1 := watchTaskDelete(t, watch)
	deletedTask2 := watchTaskDelete(t, watch)

	assert.Equal(t, api.TaskStateDead, deletedTask1.Status.State)
	assert.Equal(t, "original", deletedTask1.Annotations.Name)
	assert.Equal(t, api.TaskStateDead, deletedTask2.Status.State)
	assert.Equal(t, "original", deletedTask2.Annotations.Name)

	var foundTasks []*api.Task
	s.View(func(tx store.ReadTx) {
		foundTasks, err = store.FindTasks(tx, store.All)
	})
	assert.NoError(t, err)
	assert.Len(t, foundTasks, 4)

	taskReaper.Stop()
	orchestrator.Stop()
}