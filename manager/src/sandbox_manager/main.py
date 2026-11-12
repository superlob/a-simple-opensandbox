"""入口：uvicorn --factory sandbox_manager.main:create_app"""

from __future__ import annotations

import logging
import sys

from sandbox_manager.api.app import create_app as _create_app


class _JsonFormatter(logging.Formatter):
    _SKIP = set(vars(logging.makeLogRecord({})).keys()) | {"message", "asctime"}

    def format(self, record: logging.LogRecord) -> str:
        import json

        data = {
            "time": self.formatTime(record),
            "level": record.levelname,
            "logger": record.name,
            "msg": record.getMessage(),
        }
        for key, value in record.__dict__.items():
            if key not in self._SKIP:
                data[key] = value
        if record.exc_info:
            data["exc"] = self.formatException(record.exc_info)
        return json.dumps(data, ensure_ascii=False, default=str)


def _configure_logging() -> None:
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(_JsonFormatter())
    root = logging.getLogger()
    root.handlers[:] = [handler]
    root.setLevel(logging.INFO)


def create_app():  # type: ignore[no-untyped-def]
    _configure_logging()
    return _create_app()
