package redis_test

import (
	"fmt"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/redis"
)

// signalsFor builds n signals at distinct microsecond instants, as the codec
// writes them, so that a decoded message compares equal to what was sent.
func signalsFor(n int) []notify.Signal {
	base := time.Date(2026, 9, 14, 8, 30, 0, 0, time.UTC)
	signals := make([]notify.Signal, n)

	for i := range signals {
		signals[i] = notify.Signal{
			Recipient: fmt.Sprintf("user-%04d", i),
			Change:    notify.ChangeCreated,
			At:        base.Add(time.Duration(i) * time.Microsecond),
		}
	}

	return signals
}

func TestBroadcast(t *testing.T) {
	t.Parallel()

	client := redis.RunTestRedis(t)

	// unreachable points at a port nothing listens on.
	unreachable := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	t.Cleanup(func() { _ = unreachable.Close() })

	type testCase struct {
		name    string
		client  goredis.UniversalClient
		signals []notify.Signal
		// assert receives the messages a raw subscriber saw on the channel, and
		// how long the broadcast took.
		assert func(t *testing.T, messages [][]notify.Signal, took time.Duration, err error)
	}

	cases := []testCase{
		{
			name:    "one signal is one message in the notify format",
			client:  client,
			signals: signalsFor(1),
			assert: func(t *testing.T, messages [][]notify.Signal, _ time.Duration, err error) {
				require.NoError(t, err)
				assert.Equal(t, [][]notify.Signal{signalsFor(1)}, messages)
			},
		},
		{
			name:    "a broadcast larger than one message is split at the maximum",
			client:  client,
			signals: signalsFor(2*redis.MaxSignalsPerMessage + 1),
			assert: func(t *testing.T, messages [][]notify.Signal, _ time.Duration, err error) {
				require.NoError(t, err)

				all := signalsFor(2*redis.MaxSignalsPerMessage + 1)
				assert.Equal(t, [][]notify.Signal{
					all[:redis.MaxSignalsPerMessage],
					all[redis.MaxSignalsPerMessage : 2*redis.MaxSignalsPerMessage],
					all[2*redis.MaxSignalsPerMessage:],
				}, messages)
			},
		},
		{
			name:    "no signals publish nothing",
			client:  client,
			signals: nil,
			assert: func(t *testing.T, messages [][]notify.Signal, _ time.Duration, err error) {
				require.NoError(t, err)
				assert.Empty(t, messages)
			},
		},
		{
			name:    "an unreachable broker is a publish error within the timeout",
			client:  unreachable,
			signals: signalsFor(1),
			assert: func(t *testing.T, _ [][]notify.Signal, took time.Duration, err error) {
				require.ErrorIs(t, err, redis.ErrPublish)

				var publish *redis.PublishError
				require.ErrorAs(t, err, &publish)
				assert.Equal(t, 1, publish.Signals)
				assert.Less(t, took, 2*time.Second)
			},
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			channel := fmt.Sprintf("test.broadcast.%d", i)

			subscriber := client.Subscribe(t.Context(), channel)
			t.Cleanup(func() { _ = subscriber.Close() })

			_, err := subscriber.Receive(t.Context())
			require.NoError(t, err, "the raw subscriber is confirmed before anything is published")

			b, err := redis.NewBroadcaster(tc.client, redis.WithChannel(channel), redis.WithPublishTimeout(500*time.Millisecond))
			require.NoError(t, err)

			start := time.Now()
			err = b.Broadcast(t.Context(), tc.signals)
			took := time.Since(start)

			// A sentinel published after the broadcast marks the end of what it
			// sent, so the test reads exactly the broadcast's messages.
			require.NoError(t, client.Publish(t.Context(), channel, "end").Err())

			var messages [][]notify.Signal

			for {
				msg, receiveErr := subscriber.ReceiveMessage(t.Context())
				require.NoError(t, receiveErr)

				if msg.Payload == "end" {
					break
				}

				decoded, decodeErr := notify.DecodeSignals([]byte(msg.Payload))
				require.NoError(t, decodeErr, "every message is in the notify signal format")

				messages = append(messages, decoded)
			}

			tc.assert(t, messages, took, err)
		})
	}
}
