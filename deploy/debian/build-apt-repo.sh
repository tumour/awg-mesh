#!/bin/sh
# build-apt-repo.sh — собирает подписанный apt-репозиторий (Debian/Ubuntu) из
# готовых .deb-пакетов (см. `make package-all`).
#
# Раскладка по стандарту apt (pool + dists), индексы через apt-ftparchive,
# Release подписан OpenPGP-ключом (InRelease + detached Release.gpg):
#
#   <OUT>/pool/<COMPONENT>/m/meshd/meshd_<VERSION>_<arch>.deb
#   <OUT>/dists/<SUITE>/<COMPONENT>/binary-<arch>/Packages{,.gz}
#   <OUT>/dists/<SUITE>/{Release,InRelease,Release.gpg}
#   <OUT>/meshd-archive-keyring.asc                 (публичный ключ для клиентов)
#
# Дерево публикуется на GitHub Pages (в CI OUT=public/debian, поэтому на хосте
# путь включает /debian/); подключается строкой
#   deb [signed-by=/etc/apt/keyrings/awg-mesh.asc] https://<host>/debian <SUITE> <COMPONENT>
# (точные команды — см. README, секция про apt-репозиторий).
#
# Suite зафиксирован (`stable`), а не codename дистрибутива: один пакет (статический
# Go-бинарь + iproute2) ставится и на Debian, и на Ubuntu любых версий — дробить
# по релизам нечего. Клиент пишет `... stable main` независимо от своего кодового имени.
#
# Требует: apt-ftparchive (пакет apt-utils) + gpg на хосте.
#
# Переменные окружения:
#   VERSION       (обяз.) версия для лога/проверки (сами .deb берутся из DIST)
#   GPG_KEY_FILE  (обяз.) приватный OpenPGP-ключ (ASCII-armored), которым
#                 подписываем Release; импортируется в эфемерный GNUPGHOME,
#                 пользовательский keyring не трогается
#   DIST          каталог с готовыми .deb (по умолчанию ./dist)
#   OUT           выходной каталог дерева репозитория (по умолчанию ./apt-repo)
#   SUITE         имя suite (по умолчанию stable)
#   COMPONENT     компонент (по умолчанию main)
#   ARCHES        список Debian-арок через пробел (по умолчанию "amd64 arm64")
set -eu

VERSION=${VERSION:?set VERSION (e.g. 0.4.2)}
GPG_KEY_FILE=${GPG_KEY_FILE:?set GPG_KEY_FILE (path to armored private OpenPGP key)}
DIST=${DIST:-dist}
OUT=${OUT:-apt-repo}
SUITE=${SUITE:-stable}
COMPONENT=${COMPONENT:-main}
ARCHES=${ARCHES:-amd64 arm64}

command -v apt-ftparchive >/dev/null 2>&1 || {
	echo "error: apt-ftparchive not found (install apt-utils)" >&2; exit 1; }
command -v gpg >/dev/null 2>&1 || { echo "error: gpg not found" >&2; exit 1; }
[ -f "$GPG_KEY_FILE" ] || { echo "error: GPG_KEY_FILE '$GPG_KEY_FILE' not found" >&2; exit 1; }

echo "==> building apt repo: version=$VERSION out=$OUT suite=$SUITE arches='$ARCHES'"
rm -rf "$OUT"
mkdir -p "$OUT/pool/$COMPONENT/m/meshd"

# 1) Раскладываем .deb в pool/. Имя пула по source-name (meshd → m/meshd) — стандарт.
found=0
for arch in $ARCHES; do
	for deb in "$DIST"/meshd_*_"$arch".deb; do
		[ -f "$deb" ] || continue
		cp "$deb" "$OUT/pool/$COMPONENT/m/meshd/"
		echo "  pooled $(basename "$deb")"
		found=1
	done
done
[ "$found" = 1 ] || { echo "error: no .deb in '$DIST' for arches '$ARCHES'" >&2; exit 1; }

# 2) Per-arch Packages{,.gz}. `apt-ftparchive packages` не фильтрует по арке (свалил
#    бы оба .deb в кучу), а config-режим `generate` тянет cache-db и заранее
#    созданные каталоги. Поэтому сканируем pool один раз и режем по полю
#    Architecture (awk в paragraph-mode). Filename выходит относительным к корню
#    репо (cd в OUT) — ровно как нужно apt-клиенту.
allpkgs=$( cd "$OUT" && apt-ftparchive packages "pool/$COMPONENT" )
for arch in $ARCHES; do
	bindir="$OUT/dists/$SUITE/$COMPONENT/binary-$arch"
	mkdir -p "$bindir"
	printf '%s\n' "$allpkgs" | awk -v want="Architecture: $arch" '
		BEGIN { RS=""; FS="\n"; ORS="\n\n" }
		{ for (i = 1; i <= NF; i++) if ($i == want) { print; break } }
	' > "$bindir/Packages"
	gzip -9c "$bindir/Packages" > "$bindir/Packages.gz"
	echo "  indexed binary-$arch ($(grep -c '^Package:' "$bindir/Packages") pkgs)"
done

# 3) Release-файл (с контрольными суммами всех Packages) для этого suite.
apt-ftparchive \
	-o APT::FTPArchive::Release::Origin="awg-mesh" \
	-o APT::FTPArchive::Release::Label="awg-mesh" \
	-o APT::FTPArchive::Release::Suite="$SUITE" \
	-o APT::FTPArchive::Release::Codename="$SUITE" \
	-o APT::FTPArchive::Release::Components="$COMPONENT" \
	-o APT::FTPArchive::Release::Architectures="$ARCHES" \
	-o APT::FTPArchive::Release::Description="awg-mesh (meshd) apt repository" \
	release "$OUT/dists/$SUITE" > "$OUT/dists/$SUITE/Release"

# 4) Подпись Release. Приватный ключ импортируем в ЭФЕМЕРНЫЙ GNUPGHOME (mktemp),
#    чтобы не засорять и не зависеть от пользовательского keyring'а.
GNUPGHOME_TMP=$(mktemp -d)
GNUPGHOME="$GNUPGHOME_TMP"; export GNUPGHOME
chmod 700 "$GNUPGHOME"
# Чистим эфемерный keyring и при ошибке тоже.
trap 'rm -rf "$GNUPGHOME_TMP"' EXIT INT TERM

gpg --batch --quiet --import "$GPG_KEY_FILE"
KEYID=$(gpg --list-secret-keys --with-colons | awk -F: '/^sec:/{print $5; exit}')
[ -n "$KEYID" ] || { echo "error: no secret key in $GPG_KEY_FILE" >&2; exit 1; }

REL="$OUT/dists/$SUITE/Release"
# InRelease — inline-подписанный Release (предпочтительно современным apt);
# Release.gpg — detached-подпись (совместимость со старыми клиентами).
gpg --batch --yes --pinentry-mode loopback --default-key "$KEYID" \
	--clearsign -o "$OUT/dists/$SUITE/InRelease" "$REL"
gpg --batch --yes --pinentry-mode loopback --default-key "$KEYID" \
	-abs -o "$OUT/dists/$SUITE/Release.gpg" "$REL"

# Публичный ключ для клиентов (signed-by=). Тот же ключ коммитим в репо как
# deploy/debian/meshd-archive-keyring.asc — в CI берётся именно он.
gpg --export --armor "$KEYID" > "$OUT/meshd-archive-keyring.asc"

echo "==> done: $OUT (signed by $KEYID)"
echo "    dists/$SUITE/{Release,InRelease,Release.gpg} + meshd-archive-keyring.asc"
