from dataclasses import dataclass, field


@dataclass
class Customer:
    id: str
    email: str
    currency: str = "USD"
    credit: list = field(default_factory=list)
