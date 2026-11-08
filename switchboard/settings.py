from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="SWITCHBOARD_", env_file=".env", extra="ignore")

    database_url: str = "sqlite:///./switchboard.db"
    secret_key: str = "dev-only-insecure-secret-change-me"
    config_path: str = "config.yaml"
    host: str = "127.0.0.1"
    port: int = 8000
    quota_window_hours: int = 24
    health_poll_interval_seconds: float = 60.0
    health_poll_timeout_seconds: float = 5.0
    health_stale_after_seconds: float = 30.0
