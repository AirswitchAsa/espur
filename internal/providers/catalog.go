// Package providers holds the curated list of model providers/models exposed
// in the admin UI. Data is hand-curated rather than pulled from models.dev so
// the dropdown only shows current, common variants — refresh by editing this
// file and rebuilding the image.
package providers

// Provider is one row in the dropdown's optgroup list.
type Provider struct {
	ID            string   // opencode provider id (e.g. "anthropic")
	Name          string   // display name
	EnvKey        string   // env var opencode expects for byo_key creds; empty if OAuth-only
	SupportsOAuth bool     // true if `opencode auth login <id>` can mint a token
	Models        []string // model ids under this provider, in display order
}

// Catalog is the full set of providers shown in the admin UI. Keep this list
// short — only flagship/workhorse models from each provider.
var Catalog = []Provider{
	{
		ID: "anthropic", Name: "Anthropic", EnvKey: "ANTHROPIC_API_KEY", SupportsOAuth: true,
		Models: []string{"claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5"},
	},
	{
		ID: "openai", Name: "OpenAI", EnvKey: "OPENAI_API_KEY", SupportsOAuth: true,
		Models: []string{"gpt-5.5-pro", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano"},
	},
	{
		ID: "google", Name: "Google", EnvKey: "GEMINI_API_KEY", SupportsOAuth: true,
		Models: []string{"gemini-3.5-flash", "gemini-2.5-pro", "gemini-2.5-flash"},
	},
	{
		ID: "github-copilot", Name: "GitHub Copilot", EnvKey: "", SupportsOAuth: true,
		Models: []string{"claude-opus-4.7", "claude-sonnet-4.6", "claude-haiku-4.5", "gpt-5.5", "gemini-3.5-flash"},
	},
	{
		ID: "xai", Name: "xAI", EnvKey: "XAI_API_KEY", SupportsOAuth: false,
		Models: []string{"grok-4.3"},
	},
	{
		ID: "deepseek", Name: "DeepSeek", EnvKey: "DEEPSEEK_API_KEY", SupportsOAuth: false,
		Models: []string{"deepseek-v4-pro", "deepseek-v4-flash", "deepseek-reasoner", "deepseek-chat"},
	},
	{
		ID: "mistral", Name: "Mistral", EnvKey: "MISTRAL_API_KEY", SupportsOAuth: false,
		Models: []string{"mistral-large-latest", "mistral-medium-latest", "mistral-small-latest", "codestral-latest"},
	},
	{
		ID: "groq", Name: "Groq", EnvKey: "GROQ_API_KEY", SupportsOAuth: false,
		Models: []string{"llama-3.3-70b-versatile", "meta-llama/llama-4-maverick-17b-128e-instruct", "moonshotai/kimi-k2-instruct-0905"},
	},
	{
		ID: "cerebras", Name: "Cerebras", EnvKey: "CEREBRAS_API_KEY", SupportsOAuth: false,
		Models: []string{"gpt-oss-120b", "zai-glm-4.7"},
	},
	{
		ID: "openrouter", Name: "OpenRouter", EnvKey: "OPENROUTER_API_KEY", SupportsOAuth: false,
		Models: []string{
			"anthropic/claude-opus-4.7",
			"anthropic/claude-sonnet-4.6",
			"openai/gpt-5.5",
			"google/gemini-3.5-flash",
			"deepseek/deepseek-v4-pro",
		},
	},
}

// Lookup finds a provider+model pair for a given opencode model string in the
// form "<provider>/<model>". Returns nil if not in the catalog.
func Lookup(modelString string) (*Provider, string) {
	for i, p := range Catalog {
		for _, m := range p.Models {
			if p.ID+"/"+m == modelString {
				return &Catalog[i], m
			}
		}
	}
	return nil, ""
}
