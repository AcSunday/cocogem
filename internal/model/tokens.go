package model

// EstimateTokens returns a conservative token estimate without a provider
// tokenizer. ASCII is counted at roughly four bytes per token; every non-ASCII
// rune is counted as one token. The estimate is deliberately not an exact
// tokenizer: CJK text often uses fewer tokens per rune, while uncommon Unicode
// or code punctuation can use more.
func EstimateTokens(text string) int {
	ascii, other := 0, 0
	for _, r := range text {
		if r < 128 {
			ascii++
			continue
		}
		other++
	}
	return (ascii+3)/4 + other
}

// InputTokenBudget returns the member's explicit host input token budget.
// Zero means the CLI's configured context window, with no host-side ceiling.
func InputTokenBudget(contextSize int) int {
	if contextSize <= 0 {
		return 0
	}
	return contextSize
}
