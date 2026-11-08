from .config import SwitchboardConfig, Target


def resolve_targets(config: SwitchboardConfig, name: str | None) -> list[Target] | None:
    if not name:
        return None
    if name in config.models:
        return list(config.models[name])
    if name in config.aliases:
        targets: list[Target] = []
        for model_name in config.aliases[name]:
            model_targets = config.models.get(model_name)
            if model_targets is not None:
                targets.extend(model_targets)
        return targets or None
    return None
