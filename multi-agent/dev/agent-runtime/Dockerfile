FROM golang:1.24-bookworm

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

WORKDIR /workspace/multi-agent
