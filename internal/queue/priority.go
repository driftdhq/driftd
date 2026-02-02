package queue

func TriggerPriority(trigger string) int {
	switch trigger {
	case "scheduled", "cron":
		return 1
	case "manual", "webhook":
		return 2
	default:
		return 2
	}
}
