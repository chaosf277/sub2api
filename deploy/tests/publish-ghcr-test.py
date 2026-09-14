#!/usr/bin/env python3
"""Check publisher build arguments without contacting Docker or a registry."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


repo_root = Path(__file__).resolve().parents[2]
with tempfile.TemporaryDirectory(prefix="publish-ghcr-test-") as directory:
    root = Path(directory)
    for name in ("deploy/publish_ghcr.sh", "backend/scripts/resolve-version.sh"):
        target = root / name
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(repo_root / name, target)
    version_file = root / "backend/cmd/server/VERSION"
    version_file.parent.mkdir(parents=True)
    version_file.write_text("0.0.1\n")
    bin_dir = root / "bin"
    bin_dir.mkdir()
    docker = bin_dir / "docker"
    docker.write_text(
        "#!/usr/bin/env python3\n"
        "import json, os, sys\n"
        "if sys.argv[1:3] == ['buildx', 'build']:\n"
        "    with open(os.environ['BUILD_ARGS_FILE'], 'w') as output:\n"
        "        json.dump(sys.argv[3:], output)\n"
    )
    docker.chmod(0o755)
    args_file = root / "build-args.json"
    env = dict(os.environ, PATH=f"{bin_dir}:{os.environ['PATH']}",
               GITHUB_REPOSITORY_OWNER="TestOwner", BUILD_ARGS_FILE=str(args_file))
    subprocess.run(["git", "init", "-q", str(root)], check=True)
    subprocess.run(["git", "-C", str(root), "-c", "user.name=Test",
                    "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false",
                    "commit", "--quiet", "--allow-empty", "-m", "fixture"], check=True)
    for tag, expected_version in (("latest", "0.0.1"), ("v9.8.7", "9.8.7")):
        if tag != "latest":
            subprocess.run(["git", "-C", str(root), "tag", tag], check=True)
        subprocess.run(["bash", str(root / "deploy/publish_ghcr.sh"), tag, "linux/amd64"],
                       env=env, check=True, stdout=subprocess.DEVNULL)
        args = json.loads(args_file.read_text())
        assert args[args.index("--platform") + 1] == "linux/amd64", args
        assert f"ghcr.io/testowner/sub2api:{tag}" in args, args
        build_args = [args[i + 1] for i, arg in enumerate(args) if arg == "--build-arg"]
        assert f"VERSION={expected_version}" in build_args, build_args
    print("publish GHCR tests passed (untagged and tagged checkout)")
