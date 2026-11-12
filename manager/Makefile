# 配置从仓库根目录的 SANDBOX.env 读取，不在代码中硬编码。
ENV_FILE ?= ../SANDBOX.env
RUN = set -a; . $(ENV_FILE); set +a;

.PHONY: dev test lint typecheck build-web integration

dev:
	$(RUN) uv run uvicorn sandbox_manager.main:create_app --factory --host $$MANAGER_BIND_HOST --port $$MANAGER_PORT

test:
	uv run pytest

lint:
	uv run ruff check src tests

typecheck:
	uv run mypy src

build-web:
	cd web && npm ci && npm run build

integration:
	$(RUN) uv run pytest -m integration
