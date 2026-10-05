#!/usr/bin/env bash
set -Eeuo pipefail

# check-version.sh 在实际发布前确认目标标签仍是最高正式版本并指向构建提交。
expected_tag="${1:?用法：check-version.sh <tag> <sha> [tag-list-file]>}"
expected_sha="${2:?用法：check-version.sh <tag> <sha> [tag-list-file]>}"
tag_list_file="${3:-}"
if [ -n "$tag_list_file" ]; then
  mapfile -t tags < "$tag_list_file"
else
  mapfile -t tags < <(git tag --list)
fi
newest_tag=""
for tag in "${tags[@]}"; do
  if [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] && { [ -z "$newest_tag" ] || printf '%s\n' "$newest_tag" "$tag" | sort -V | tail -n 1 | grep -qx "$tag"; }; then
    newest_tag="$tag"
  fi
done
if [ "$newest_tag" != "$expected_tag" ]; then
  echo "最高正式版本为 $newest_tag，拒绝发布 $expected_tag。" >&2
  exit 1
fi
actual_sha="$(git rev-list -n 1 "$expected_tag")"
if [ "$actual_sha" != "$expected_sha" ]; then
  echo "标签 $expected_tag 未指向构建提交。" >&2
  exit 1
fi
