import unittest
from concurrent.futures import ThreadPoolExecutor
import wendy_core


class CoreTest(unittest.TestCase):
    def test_contract(self):
        self.assertEqual(wendy_core.__all__, ["WendyHelloWorld"])
        self.assertEqual(wendy_core.WendyHelloWorld(), 42)

    def test_concurrent_calls(self):
        with ThreadPoolExecutor(max_workers=4) as executor:
            values = list(executor.map(lambda _: wendy_core.WendyHelloWorld(), range(100)))
        self.assertEqual(values, [42] * 100)


if __name__ == "__main__":
    unittest.main()
