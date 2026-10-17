package queue

import "maps"

const observationShardCount = 256

type observationShards [observationShardCount]map[int]PRObserved

// Both the table and its shards are immutable. A write copies the table and one
// bucket, leaving State cheap to pass by value. Retained observations can grow
// with history without requiring a full-map copy on every CI event.
type observations struct{ shards *observationShards }

func (o *observations) get(pr int) (PRObserved, bool) {
	if o.shards == nil {
		return PRObserved{}, false
	}
	v, ok := o.shards[uint(pr)%observationShardCount][pr]
	return v, ok
}
func (o *observations) put(pr int, v PRObserved) {
	next := new(observationShards)
	if o.shards != nil {
		*next = *o.shards
	}
	i := uint(pr) % observationShardCount
	shard := maps.Clone(next[i])
	if shard == nil {
		shard = make(map[int]PRObserved)
	}
	shard[pr] = v
	next[i] = shard
	o.shards = next
}
func (o *observations) remove(pr int) {
	if _, ok := o.get(pr); !ok {
		return
	}
	next := new(observationShards)
	*next = *o.shards
	i := uint(pr) % observationShardCount
	shard := maps.Clone(next[i])
	delete(shard, pr)
	if len(shard) == 0 {
		shard = nil
	}
	next[i] = shard
	o.shards = next
}
func (o *observations) snapshot() map[int]PRObserved {
	result := make(map[int]PRObserved)
	if o.shards != nil {
		for _, shard := range o.shards {
			maps.Copy(result, shard)
		}
	}
	return result
}
func restoreObservations(values map[int]PRObserved) observations {
	var o observations
	if len(values) == 0 {
		return o
	}
	o.shards = new(observationShards)
	for pr, v := range values {
		i := uint(pr) % observationShardCount
		if o.shards[i] == nil {
			o.shards[i] = make(map[int]PRObserved)
		}
		o.shards[i][pr] = v
	}
	return o
}
