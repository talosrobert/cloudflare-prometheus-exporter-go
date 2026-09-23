package cloudflareapi

// estimatedCount scales a sampled *AdaptiveGroups row count up to the number
// of real events it stands for. Cloudflare stores each event's sample
// interval (the reciprocal of its inclusion probability, ≥ 1), so a group's
// count × avg(sampleInterval) is the estimated event total — exactly how
// Cloudflare's own dashboard reports sampled data
// (https://developers.cloudflare.com/analytics/sampling/). Rows that carry no
// sampleInterval decode as 0 and are taken at face value.
func estimatedCount(count, sampleInterval float64) float64 {
	if sampleInterval <= 0 {
		return count
	}
	return count * sampleInterval
}
