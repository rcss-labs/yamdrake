// Package wake carries wake-ups between the parts of the orchestrator and the
// runners, so they do not have to wait for the next poll.
package wake

// Topics a wake-up can be published on. The id published with a topic is the
// run the news is about.
const (
	TopicLegs, TopicRuns, TopicLogs = "legs", "runs", "logs"
)
