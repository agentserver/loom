"""e2e fixtures: assert prod_test fleet is up."""
import pytest

import loom


@pytest.fixture(scope="session")
def slave_local_prod():
    """Sanity-check that slave-local-prod is visible. Skip if not."""
    with loom.workflow(goal="probe fleet") as wf:
        try:
            names = wf.list_slaves()
        except Exception as e:
            pytest.skip(f"driver not reachable: {e}")
    if "slave-local-prod" not in names:
        pytest.skip(f"slave-local-prod not visible; got {names}")
    return "slave-local-prod"
