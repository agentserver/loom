FROM golang:1.26-bookworm

RUN apt-get update && apt-get install -y \
    ca-certificates \
    curl \
    git \
    nodejs \
    npm \
    python3-numpy \
    sqlite3 \
    zsh \
  && rm -rf /var/lib/apt/lists/*

RUN npm install -g @anthropic-ai/claude-code @openai/codex

ENV ANTHROPIC_BASE_URL=https://code.ai.cs.ac.cn

RUN mkdir -p /root \
  && printf '{"hasCompletedOnboarding":true}\n' > /root/.claude.json

RUN mkdir -p /root/.claude \
  && printf '%s\n' \
    '{' \
    '  "permissions": {' \
    '    "allow": [' \
    '      "Read",' \
    '      "Write",' \
    '      "Edit",' \
    '      "Bash(python *)",' \
    '      "Bash(python3 *)",' \
    '      "Bash(curl *)",' \
    '      "Bash(chmod *)",' \
    '      "Bash(ls *)",' \
    '      "Bash(cat *)",' \
    '      "Bash(pwd)",' \
    '      "Bash(mkdir *)"' \
    '    ],' \
    '    "deny": []' \
    '  }' \
    '}' > /root/.claude/settings.json

# Test/runtime containers can mount a host-provided /root/.zshrc containing:
#   export ANTHROPIC_BASE_URL=https://code.ai.cs.ac.cn
#   export ANTHROPIC_API_KEY=<your-api-key>
# zsh reads zshenv for non-interactive commands such as `zsh -lc "claude"`.
# Only source ANTHROPIC_* assignment lines so local shell framework setup does
# not pollute stdio MCP traffic.
RUN printf '\nif [ -f /root/.zshrc ]; then\n  _anthropic_env=/tmp/anthropic-env.$$\n  grep -E "^(export[[:space:]]+)?(ANTHROPIC_(BASE_URL|API_KEY)|OPENAI_API_KEY)=" /root/.zshrc | sed -E "s/^export[[:space:]]+//" > "$_anthropic_env" || true\n  if [ -s "$_anthropic_env" ]; then\n    set -a\n    source "$_anthropic_env"\n    set +a\n  fi\n  rm -f "$_anthropic_env"\nfi\n' >> /etc/zsh/zshenv

# Default codex CLI config — model_provider points at the self-hosted modelserver
# endpoint at code.ai.cs.ac.cn/v1 (OpenAI-compatible). Operators can override by
# mounting a different /root/.codex/config.toml at runtime. The mcp_servers
# table is intentionally empty here — driver containers add their own
# [mcp_servers.driver] block via project-scoped .codex/config.toml.
RUN mkdir -p /root/.codex \
  && printf '%s\n' \
    'model_provider = "modelserver"' \
    'model = "gpt-5.4"' \
    '' \
    '[model_providers.modelserver]' \
    'name = "modelserver"' \
    'base_url = "https://code.ai.cs.ac.cn/v1"' \
    'env_key = "OPENAI_API_KEY"' \
    'wire_api = "responses"' > /root/.codex/config.toml

WORKDIR /workspace/multi-agent
