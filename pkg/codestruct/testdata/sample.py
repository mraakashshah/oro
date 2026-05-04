MY_CONST = "hello"
_my_private_const = 42


class MyClass:
    def public_method(self) -> str:
        return "hello"

    def _private_method(self) -> None:
        pass


class _PrivateClass:
    pass


def public_func(x: int) -> str:
    return str(x)


async def async_func(x: int) -> str:
    return str(x)


def _private_func() -> None:
    pass


@staticmethod
def decorated_func() -> None:
    pass
