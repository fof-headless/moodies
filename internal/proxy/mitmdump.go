package proxy

// DefaultTargetHosts is the canonical host list used by both the PAC file
// generator and the Go proxy's matchTarget logic. Must stay in sync with
// pacTemplate in pac.go.
//
// Convention: entries starting with '.' are suffix-matched (".foo.com" matches
// "api.foo.com" and "foo.com"). Entries without a leading dot require an exact
// hostname match.
func DefaultTargetHosts() []string {
	return []string{
		// Anthropic / Claude
		".anthropic.com",
		"claude.ai",
		".claude.ai",
		".claudeusercontent.com",

		// OpenAI
		"api.openai.com",
		"openai.com",
		".openai.com",
		".openai.azure.com", // Azure OpenAI (wildcard: <resource>.openai.azure.com)

		// Google Gemini
		"generativelanguage.googleapis.com",
		".generativelanguage.googleapis.com",
		"gemini.google.com",
		"aistudio.google.com",

		// Mistral
		"api.mistral.ai",
		".mistral.ai",

		// Cohere
		"api.cohere.ai",
		"api.cohere.com",

		// Groq
		"api.groq.com",

		// Together AI
		"api.together.xyz",
		"api.together.ai",
	}
}
