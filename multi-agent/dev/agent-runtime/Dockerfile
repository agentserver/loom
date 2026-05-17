FROM golang:1.26-bookworm

RUN apt-get update && apt-get install -y \
    ca-certificates \
    curl \
    git \
    nodejs \
    npm \
    sqlite3 \
    zsh \
  && rm -rf /var/lib/apt/lists/*

RUN npm install -g @anthropic-ai/claude-code

ENV ANTHROPIC_BASE_URL=https://code.ai.cs.ac.cn

RUN mkdir -p /root \
  && printf '{"hasCompletedOnboarding":true}\n' > /root/.claude.json

# Test/runtime containers can mount a host-provided /root/.zshrc containing:
#   export ANTHROPIC_BASE_URL=https://code.ai.cs.ac.cn
#   export ANTHROPIC_API_KEY=<your-api-key>
# zsh reads zshenv for non-interactive commands such as `zsh -lc "claude"`.
# Only source ANTHROPIC_* assignment lines so local shell framework setup does
# not pollute stdio MCP traffic.
RUN printf '\nif [ -f /root/.zshrc ]; then\n  _anthropic_env=/tmp/anthropic-env.$$\n  grep -E "^(export[[:space:]]+)?ANTHROPIC_(BASE_URL|API_KEY)=" /root/.zshrc | sed -E "s/^export[[:space:]]+//" > "$_anthropic_env" || true\n  if [ -s "$_anthropic_env" ]; then\n    set -a\n    source "$_anthropic_env"\n    set +a\n  fi\n  rm -f "$_anthropic_env"\nfi\n' >> /etc/zsh/zshenv

WORKDIR /workspace/multi-agent
