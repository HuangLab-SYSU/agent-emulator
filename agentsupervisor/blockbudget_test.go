package agentsupervisor

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestBlockBudgetGlobalReservations(t *testing.T) {
	b := NewBlockBudget(4)
	for shard := int64(0); shard < 4; shard++ {
		require.NoError(t, b.Request(shard, 1))
	}
	b.Limit(2)
	s1, id1, ok := b.Grant()
	require.True(t, ok)
	s2, id2, ok := b.Grant()
	require.True(t, ok)
	require.NotEqual(t, s1, s2)
	_, _, ok = b.Grant()
	require.False(t, ok, "in-flight blocks must already consume quota")
	require.False(t, b.Drained())
	require.NoError(t, b.Complete(s2, id2))
	require.NoError(t, b.Complete(s1, id1))
	require.NoError(t, b.Complete(s1, id1), "duplicate completion is idempotent")
	require.EqualValues(t, 2, b.Committed)
	require.True(t, b.Drained())
	_, _, ok = b.Grant()
	require.False(t, ok, "committing does not replenish the decision budget")
	b.Release()
	_, _, ok = b.Grant()
	require.True(t, ok, "decision completion resumes remaining shards")
}

func TestBlockBudgetHoldZeroAndValidation(t *testing.T) {
	b := NewBlockBudget(2)
	require.Error(t, b.Request(2, 1))
	require.Error(t, b.Request(0, 0))
	require.NoError(t, b.Request(0, 1))
	require.NoError(t, b.Request(0, 1))
	require.Error(t, b.Request(0, 2))
	b.Hold()
	_, _, ok := b.Grant()
	require.False(t, ok)
	b.Limit(0)
	_, _, ok = b.Grant()
	require.False(t, ok)
	b.Release()
	shard, id, ok := b.Grant()
	require.True(t, ok)
	require.Error(t, b.Request(0, 2))
	require.Error(t, b.Complete(shard, id+1))
	require.NoError(t, b.Complete(shard, id))
	require.NoError(t, b.Request(shard, id))
	_, _, ok = b.Grant()
	require.False(t, ok, "old requests must not issue another block")
}
