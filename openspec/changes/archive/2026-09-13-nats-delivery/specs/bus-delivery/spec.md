## REMOVED Requirements

### Requirement: The stream is unbounded unless the host bounds it

**Reason**: Stream retention is a property of the Redis Stream sink. A bus sink for a broker whose retention lives in its own configuration, such as a NATS JetStream stream, cannot meet it, so it does not belong in the contract every bus sink shares.
**Migration**: Moved unchanged to the `redis-delivery` capability. The Redis sink's behaviour is unaffected.

### Requirement: The host can bound the stream by length

**Reason**: Redis-specific: it bounds a Redis Stream on the publish path, which not every bus has.
**Migration**: Moved unchanged to the `redis-delivery` capability. The Redis sink's behaviour is unaffected.

### Requirement: The host can bound the stream by age

**Reason**: Redis-specific: it trims a Redis Stream by entry identifier age, which not every bus has.
**Migration**: Moved unchanged to the `redis-delivery` capability. The Redis sink's behaviour is unaffected.

### Requirement: The host chooses how trimming treats consumer groups

**Reason**: Redis-specific: trim modes and consumer-group pending lists are Redis Stream concepts with no equivalent on other buses.
**Migration**: Moved unchanged to the `redis-delivery` capability. The Redis sink's behaviour is unaffected.

### Requirement: Invalid retention configuration is rejected at construction

**Reason**: It validates the Redis-specific retention settings above, so it moves with them.
**Migration**: Moved unchanged to the `redis-delivery` capability. The Redis sink's behaviour is unaffected.
