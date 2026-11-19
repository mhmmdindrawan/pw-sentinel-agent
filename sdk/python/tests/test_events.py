"""events.py must mirror agent/registry.yaml.

Runs only inside the Sentinel repo (the registry is not vendored with the
SDK into kiosk apps). Stdlib only, Python 3.6 compatible.
"""
import os
import re
import unittest

from parkways_sentinel import events

REGISTRY = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                        "..", "..", "..", "agent", "registry.yaml")


def load_registry(path):
    """Return {event name: severity} from the registry's simple layout."""
    found = {}
    name = None
    with open(path, encoding="utf-8") as f:
        for line in f:
            m = re.match(r"^  ([A-Z][A-Z0-9_]*):", line)
            if m:
                name = m.group(1)
                found[name] = None
                continue
            m = re.match(r"^    severity:\s*([A-Z]+)", line)
            if m and name:
                found[name] = m.group(1)
    return found


@unittest.skipUnless(os.path.exists(REGISTRY), "agent/registry.yaml not next to this SDK")
class EventsMatchRegistry(unittest.TestCase):
    def setUp(self):
        self.registry = load_registry(REGISTRY)

    def test_registry_parsed(self):
        self.assertIn("PAYMENT_TIMEOUT", self.registry)
        self.assertEqual(self.registry["PAYMENT_TIMEOUT"], "HIGH")
        for name, sev in self.registry.items():
            self.assertIsNotNone(sev, name + " has no severity")

    def test_every_registry_event_has_a_constant(self):
        for name in self.registry:
            self.assertEqual(getattr(events, name, None), name,
                             "events.py lacks %s = %r" % (name, name))

    def test_high_priority_matches_registry(self):
        want = set(n for n, s in self.registry.items() if s in ("HIGH", "CRITICAL"))
        self.assertEqual(set(events.HIGH_PRIORITY), want)


if __name__ == "__main__":
    unittest.main()
