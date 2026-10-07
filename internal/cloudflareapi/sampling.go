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

// minRawSamplesForConfidence is the fewest sampled records whose scaled total
// is worth believing; below it the relative error (~1/√n) swamps the estimate.
const minRawSamplesForConfidence = 10

// lowConfidence reports whether scaling this row would multiply a handful of
// records into an estimate that swings by orders of magnitude between windows.
func lowConfidence(count, sampleInterval float64) bool {
	return sampleInterval > 1 && count < minRawSamplesForConfidence
}
