import os
from dataclasses import dataclass, field

import yaml


@dataclass(frozen=True)
class Target:
    upstream: str
    model: str


@dataclass(frozen=True)
class Upstream:
    name: str
    base_url: str
    api_key: str | None = None


@dataclass
class SwitchboardConfig:
    upstreams: dict[str, Upstream] = field(default_factory=dict)
    models: dict[str, list[Target]] = field(default_factory=dict)
    aliases: dict[str, list[str]] = field(default_factory=dict)

    def known_model_names(self) -> list[str]:
        return sorted(set(self.models) | set(self.aliases))


def load_config(path: str) -> SwitchboardConfig:
    with open(path) as fh:
        raw = yaml.safe_load(fh) or {}

    upstreams: dict[str, Upstream] = {}
    for entry in raw.get("upstreams") or []:
        api_key = entry.get("api_key")
        upstreams[entry["name"]] = Upstream(
            name=entry["name"],
            base_url=str(entry["base_url"]).rstrip("/"),
            api_key=os.path.expandvars(str(api_key)) if api_key else None,
        )

    models: dict[str, list[Target]] = {}
    for name, targets in (raw.get("models") or {}).items():
        models[name] = [Target(upstream=t["upstream"], model=t["model"]) for t in targets]

    aliases: dict[str, list[str]] = {}
    for name, model_names in (raw.get("aliases") or {}).items():
        aliases[name] = [str(m) for m in model_names]

    return SwitchboardConfig(upstreams=upstreams, models=models, aliases=aliases)
