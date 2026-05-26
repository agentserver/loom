"""List agents the driver can see. Sanity check for prod_test wiring."""
import sys, os, json
sys.path.insert(0, os.path.dirname(__file__))
from lib import call_tool

r = call_tool("list_agents", {})
for a in r.get("agents", []):
    print(f'{a["display_name"]:25s} id={a["agent_id"][:8]} skills={",".join(a.get("skills",[]))}')
