import os
import re
import subprocess
pattern = re.compile(r"^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([a-zA-Z0-9_./-]+\))?!?: .+")
base, head = os.getenv("BASE"), os.getenv("HEAD")
revision = f"{base}..{head}" if base and head else "HEAD"
subjects = subprocess.check_output(["git", "log", "--format=%s", revision], text=True).splitlines()
if os.getenv("TITLE"):
    subjects.append(os.environ["TITLE"])
invalid = [s for s in subjects if not pattern.fullmatch(s)]
if invalid:
    raise SystemExit("Invalid Conventional Commit subjects: " + repr(invalid))
