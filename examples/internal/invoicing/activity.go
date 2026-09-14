package invoicing

// ActivityOf is the activity a task type stands for, which is what goes into
// CorrelationData.ActivityKey.
func ActivityOf(taskType string) string {
	if taskType == ReviewType {
		return ActivityReview
	}

	return ActivityApprove
}
