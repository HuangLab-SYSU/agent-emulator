package agentsupervisor

import "fmt"

// BlockBudget is owned by the supervisor's serial loop. Reservations, not
// delayed commit reports, consume budget so concurrent shards cannot overshoot.
type BlockBudget struct {
	waiting   map[int64]uint64
	inflight  map[int64]uint64
	completed map[int64]uint64
	shards    int64
	next      int64
	held      bool
	limited   bool
	remaining uint64
	Granted   uint64
	Committed uint64
}

func NewBlockBudget(shards int64) *BlockBudget {
	return &BlockBudget{
		waiting:   make(map[int64]uint64),
		inflight:  make(map[int64]uint64),
		completed: make(map[int64]uint64),
		shards:    shards,
	}
}

func (b *BlockBudget) Request(shard int64, id uint64) error {
	if shard < 0 || shard >= b.shards || id == 0 {
		return fmt.Errorf("invalid proposal permit request")
	}

	if id <= b.completed[shard] {
		return nil
	}

	if current := b.inflight[shard]; current != 0 {
		if current == id {
			return nil
		}

		return fmt.Errorf("shard %d requested a second in-flight permit", shard)
	}

	if current := b.waiting[shard]; current != 0 && current != id {
		return fmt.Errorf("shard %d changed pending permit", shard)
	}

	b.waiting[shard] = id

	return nil
}

func (b *BlockBudget) Complete(shard int64, id uint64) error {
	if shard < 0 || shard >= b.shards || id == 0 {
		return fmt.Errorf("invalid proposal completion")
	}

	if id <= b.completed[shard] {
		return nil
	}

	if b.inflight[shard] != id {
		return fmt.Errorf("unexpected proposal completion shard=%d id=%d", shard, id)
	}

	delete(b.inflight, shard)
	b.completed[shard] = id
	b.Committed++

	return nil
}
func (b *BlockBudget) Hold()          { b.held = true }
func (b *BlockBudget) Limit(n uint64) { b.held = false; b.limited = true; b.remaining = n }
func (b *BlockBudget) Release()       { b.held = false; b.limited = false }
func (b *BlockBudget) Drained() bool  { return len(b.inflight) == 0 }
func (b *BlockBudget) Grant() (int64, uint64, bool) {
	if b.held || (b.limited && b.remaining == 0) {
		return 0, 0, false
	}

	for i := int64(0); i < b.shards; i++ {
		shard := (b.next + i) % b.shards
		if id := b.waiting[shard]; id != 0 {
			delete(b.waiting, shard)

			b.inflight[shard] = id
			if b.limited {
				b.remaining--
			}

			b.Granted++
			b.next = (shard + 1) % b.shards

			return shard, id, true
		}
	}

	return 0, 0, false
}
