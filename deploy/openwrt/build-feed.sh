#!/bin/sh
# build-feed.sh — собирает подписанный apk-фид (apk-tools v3) под OpenWrt.
#
# Из готовых Go-бинарников (bin/meshd-linux-<goarch>, см. `make build-all`)
# раскладывает per-arch .apk-пакеты и подписанные индексы packages.adb в дерево:
#
#   <OUT>/<WRT_VER>/<openwrt-arch>/meshd-<VERSION>.apk
#   <OUT>/<WRT_VER>/<openwrt-arch>/packages.adb   (подписан ключом)
#
# Это дерево публикуется на GitHub Pages (в CI OUT=public/openwrt, поэтому на
# устройстве путь включает /openwrt/); фид добавляется строкой
#   https://<host>/openwrt/<WRT_VER>/$(cat /etc/apk/arch)/packages.adb
# (точный URL — см. README, секция про apk-фид).
#
# Почему per-arch, а не noarch: в ФИДЕ apk сам выбирает пакет по арке
# устройства (cat /etc/apk/arch), поэтому каждый CPU-вариант должен иметь
# правильное поле arch. Один Go-бинарь покрывает всю CPU-семью — поэтому
# ниже один goarch маппится на список конкретных OpenWrt-арок.
#
# Требует: nfpm + envsubst на хосте, apk-tools v3 (берётся из docker-образа
# alpine:edge — на хосте apk обычно нет). docker должен быть доступен.
#
# Переменные окружения:
#   VERSION            (обяз.) версия пакета, напр. 0.2.0
#   APK_SIGN_KEY_FILE  (обяз.) приватный RSA-ключ; basename ДОЛЖЕН быть
#                      awg-mesh-apk.rsa (иначе подпись индекса не совпадёт
#                      с ключом в /etc/apk/keys на устройстве)
#   WRT_VER            версия OpenWrt в пути фида (по умолчанию 25.12)
#   OUT                выходной каталог (по умолчанию ./feed)
#   APK_IMAGE          docker-образ с apk-tools v3 (по умолчанию alpine:edge)
set -eu

VERSION=${VERSION:?set VERSION (e.g. 0.2.0)}
APK_SIGN_KEY_FILE=${APK_SIGN_KEY_FILE:?set APK_SIGN_KEY_FILE (path to awg-mesh-apk.rsa)}
WRT_VER=${WRT_VER:-25.12}
OUT=${OUT:-feed}
APK_IMAGE=${APK_IMAGE:-alpine:edge}

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
NFPM_CONFIG="$REPO_ROOT/nfpm-openwrt.yaml"

# Подпись индекса (apk mkndx --sign-key) ищет публичный ключ по имени
# <basename-приватного>.pub. На устройстве он лежит как awg-mesh-apk.rsa.pub,
# значит приватный обязан называться awg-mesh-apk.rsa.
if [ "$(basename "$APK_SIGN_KEY_FILE")" != "awg-mesh-apk.rsa" ]; then
	echo "error: APK_SIGN_KEY_FILE basename must be 'awg-mesh-apk.rsa', got '$(basename "$APK_SIGN_KEY_FILE")'" >&2
	exit 1
fi

# Публичный ключ ОБЯЗАН лежать рядом с приватным: apk mkndx проверяет подписи
# входных пакетов по доверенным ключам, поэтому ниже (в docker) мы кладём этот
# .pub в /etc/apk/keys контейнера. Без него mkndx отверг бы nfpm-подписанные .apk.
if [ ! -f "$(dirname "$APK_SIGN_KEY_FILE")/awg-mesh-apk.rsa.pub" ]; then
	echo "error: public key awg-mesh-apk.rsa.pub not found next to $APK_SIGN_KEY_FILE" >&2
	exit 1
fi

# Маппинг Go GOARCH -> OpenWrt apk-арки (значения `cat /etc/apk/arch`).
# Покрываем ходовые роутерные таргеты; экзотику (mips64/ppc/riscv/loongarch,
# armv5 без vfp) намеренно пропускаем — под них нет Go-сборок в build-all.
openwrt_arches() {
	case "$1" in
	amd64)  echo "x86_64" ;;
	arm64)  echo "aarch64_cortex-a53 aarch64_cortex-a72 aarch64_cortex-a76 aarch64_generic" ;;
	armv7)  echo "arm_cortex-a7 arm_cortex-a7_neon-vfpv4 arm_cortex-a7_vfpv4 arm_cortex-a9 arm_cortex-a9_neon arm_cortex-a9_vfpv3-d16 arm_cortex-a15_neon-vfpv4 arm_cortex-a5_vfpv4 arm_cortex-a8_vfpv3" ;;
	mipsle) echo "mipsel_24kc mipsel_74kc mipsel_mips32" ;;
	mips)   echo "mips_24kc mips_mips32" ;;
	*) echo "" ;;
	esac
}

echo "==> building apk feed: version=$VERSION wrt=$WRT_VER out=$OUT"
rm -rf "$OUT"

# 1) Генерим per-arch .apk через nfpm (на хосте). Для каждой OpenWrt-арки —
#    свой пакет с правильным полем arch, но тем же Go-бинарём CPU-семьи.
for goarch in amd64 arm64 armv7 mipsle mips; do
	bin="$REPO_ROOT/bin/meshd-linux-$goarch"
	if [ ! -f "$bin" ]; then
		echo "warn: $bin missing — skipping $goarch (run 'make build-all')" >&2
		continue
	fi
	for apkarch in $(openwrt_arches "$goarch"); do
		dir="$OUT/$WRT_VER/$apkarch"
		mkdir -p "$dir"
		# envsubst подставляет GOARCH (выбор бинаря), APK_ARCH (поле arch),
		# VERSION, APK_SIGN_KEY_FILE в nfpm-конфиг.
		GOARCH="$goarch" APK_ARCH="$apkarch" VERSION="$VERSION" \
			APK_SIGN_KEY_FILE="$APK_SIGN_KEY_FILE" \
			envsubst < "$NFPM_CONFIG" > "$dir/.nfpm.yaml"
		nfpm pkg --config "$dir/.nfpm.yaml" --packager apk \
			--target "$dir/meshd-$VERSION.apk" >/dev/null
		rm -f "$dir/.nfpm.yaml"
		echo "  packed $apkarch"
	done
done

# 2) Генерим+подписываем индекс packages.adb для каждой арки через apk-tools v3
#    в docker. Монтируем OUT и ключ; ключ — read-only.
keydir=$(cd "$(dirname "$APK_SIGN_KEY_FILE")" && pwd)
echo "==> indexing + signing via $APK_IMAGE"
docker run --rm \
	-v "$(cd "$OUT" && pwd):/feed" \
	-v "$keydir:/keys:ro" \
	-e WRT_VER="$WRT_VER" \
	"$APK_IMAGE" sh -euc '
	# mkndx проверяет подпись входных пакетов — доверяем нашему публичному ключу.
	cp /keys/awg-mesh-apk.rsa.pub /etc/apk/keys/awg-mesh-apk.rsa.pub
	for archdir in /feed/$WRT_VER/*/; do
		cd "$archdir"
		apk mkndx --sign-key /keys/awg-mesh-apk.rsa -o packages.adb meshd-*.apk >/dev/null
		echo "  indexed $(basename "$archdir")"
	done
'

echo "==> done: $OUT/$WRT_VER/<arch>/{meshd-$VERSION.apk,packages.adb}"
