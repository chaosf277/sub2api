#!/usr/bin/env bash
# 构建并推送 sub2api 镜像到 GitHub Container Registry (ghcr.io)
# 使用方法:
#   bash deploy/publish_ghcr.sh [tag] [platform]
# 示例:
#   bash deploy/publish_ghcr.sh latest linux/amd64,linux/arm64
#   bash deploy/publish_ghcr.sh v0.2.4 linux/amd64

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# 获取当前 git 仓库的 owner（小写）
GITHUB_OWNER="${GITHUB_REPOSITORY_OWNER:-}"
if [ -z "$GITHUB_OWNER" ]; then
    # 尝试从 git remote 中获取
    REMOTE_URL=$(git -C "$REPO_ROOT" remote get-url origin 2>/dev/null || echo "")
    if [[ "$REMOTE_URL" =~ github\.com[:/]([^/]+)/ ]]; then
        GITHUB_OWNER="${BASH_REMATCH[1]}"
    fi
fi

if [ -z "$GITHUB_OWNER" ]; then
    echo "错误: 未能获取到 GitHub 用户名/组织名。请设置环境变量 GITHUB_REPOSITORY_OWNER。"
    exit 1
fi

GITHUB_OWNER_LOWER=$(echo "$GITHUB_OWNER" | tr '[:upper:]' '[:lower:]')
IMAGE_NAME="ghcr.io/${GITHUB_OWNER_LOWER}/sub2api"

TAG="${1:-latest}"
PLATFORMS="${2:-linux/amd64,linux/arm64}"
COMMIT=$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo "unknown")

echo "=========================================="
echo "准备构建并发布镜像到 GHCR"
echo "镜像仓库: ${IMAGE_NAME}"
echo "标签版本: ${TAG}"
echo "目标架构: ${PLATFORMS}"
echo "Git 提交: ${COMMIT}"
echo "=========================================="

# 检查 docker buildx
if ! docker buildx version >/dev/null 2>&1; then
    echo "错误: 需要安装 Docker Buildx 插件以支持多架构构建。"
    exit 1
fi

# 创建或使用已有的 buildx builder
BUILDER_NAME="sub2api-builder"
if ! docker buildx inspect "$BUILDER_NAME" >/dev/null 2>&1; then
    echo "创建并切换到新的 buildx builder: ${BUILDER_NAME}..."
    docker buildx create --name "$BUILDER_NAME" --use
else
    docker buildx use "$BUILDER_NAME"
fi

# 执行多平台构建与推送
docker buildx build \
    --platform "${PLATFORMS}" \
    -t "${IMAGE_NAME}:${TAG}" \
    -t "${IMAGE_NAME}:${COMMIT}" \
    --build-arg GOPROXY=https://proxy.golang.org,direct \
    --build-arg COMMIT="${COMMIT}" \
    --push \
    -f "${REPO_ROOT}/Dockerfile" \
    "${REPO_ROOT}"

echo "=========================================="
echo "发布成功！镜像地址:"
echo "  ${IMAGE_NAME}:${TAG}"
echo "  ${IMAGE_NAME}:${COMMIT}"
echo "=========================================="
