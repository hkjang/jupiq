#!/usr/bin/env node

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(scriptDirectory, '..');
const manifestPath = path.join(repositoryRoot, 'docs/assets/screenshots/manifest.json');
const galleryPath = path.join(repositoryRoot, 'docs/screenshots/index.html');
const homePath = path.join(repositoryRoot, 'docs/index.html');
const routesPath = path.join(repositoryRoot, 'web/src/App.tsx');
const screenshotDirectory = path.dirname(manifestPath);
const failures = [];

function fail(message) {
  failures.push(message);
}

function readText(filePath) {
  try {
    return fs.readFileSync(filePath, 'utf8');
  } catch (error) {
    fail(`${path.relative(repositoryRoot, filePath)} 읽기 실패: ${error.message}`);
    return '';
  }
}

function escapeRegularExpression(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function routeMatches(route, pathname) {
  if (route === '*') {
    return false;
  }

  const pattern = route
    .split('/')
    .map((segment) => (segment.startsWith(':') ? '[^/]+' : escapeRegularExpression(segment)))
    .join('/');
  return new RegExp(`^${pattern}$`).test(pathname);
}

let manifest = [];
try {
  manifest = JSON.parse(readText(manifestPath));
} catch (error) {
  fail(`docs/assets/screenshots/manifest.json JSON 파싱 실패: ${error.message}`);
}

if (!Array.isArray(manifest) || manifest.length === 0) {
  fail('스크린샷 manifest는 하나 이상의 항목을 가진 배열이어야 합니다.');
  manifest = [];
}

const filenames = new Set();
const manifestPaths = [];

for (const [index, entry] of manifest.entries()) {
  const prefix = `manifest[${index}]`;
  if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
    fail(`${prefix}는 객체여야 합니다.`);
    continue;
  }

  if (typeof entry.path !== 'string' || !entry.path.startsWith('/')) {
    fail(`${prefix}.path는 /로 시작하는 문자열이어야 합니다.`);
  } else {
    try {
      const parsed = new URL(entry.path, 'https://jupiq.invalid');
      if (parsed.hash) {
        fail(`${prefix}.path에는 fragment를 넣을 수 없습니다: ${entry.path}`);
      }
      manifestPaths.push({ ...entry, pathname: parsed.pathname });
    } catch (error) {
      fail(`${prefix}.path를 URL로 해석할 수 없습니다: ${error.message}`);
    }
  }

  if (typeof entry.file !== 'string' || !/^[a-z0-9][a-z0-9-]*\.webp$/.test(entry.file)) {
    fail(`${prefix}.file은 소문자 kebab-case .webp 파일명이어야 합니다.`);
    continue;
  }
  if (filenames.has(entry.file)) {
    fail(`중복 스크린샷 파일명: ${entry.file}`);
  }
  filenames.add(entry.file);

  if (!['desktop', 'mobile'].includes(entry.viewport)) {
    fail(`${prefix}.viewport는 desktop 또는 mobile이어야 합니다.`);
  }
  if (typeof entry.condition !== 'string' || entry.condition.trim() === '') {
    fail(`${prefix}.condition은 비어 있지 않은 문자열이어야 합니다.`);
  }

  const imagePath = path.join(screenshotDirectory, entry.file);
  if (!fs.existsSync(imagePath)) {
    fail(`manifest 파일 없음: docs/assets/screenshots/${entry.file}`);
    continue;
  }

  const descriptor = fs.openSync(imagePath, 'r');
  try {
    const header = Buffer.alloc(12);
    const bytesRead = fs.readSync(descriptor, header, 0, header.length, 0);
    const riff = header.subarray(0, 4).toString('ascii');
    const webp = header.subarray(8, 12).toString('ascii');
    if (bytesRead !== 12 || riff !== 'RIFF' || webp !== 'WEBP') {
      fail(`WebP signature 오류: docs/assets/screenshots/${entry.file}`);
    }
  } finally {
    fs.closeSync(descriptor);
  }
}

const gallery = readText(galleryPath);
const galleryFiles = [...gallery.matchAll(/src="\.\.\/assets\/screenshots\/([a-z0-9-]+\.webp)"/g)].map(
  (match) => match[1],
);
const galleryFileSet = new Set(galleryFiles);

for (const filename of filenames) {
  if (!galleryFileSet.has(filename)) {
    fail(`전체 화면 갤러리 참조 누락: ${filename}`);
  }
}
for (const filename of galleryFileSet) {
  if (!filenames.has(filename)) {
    fail(`manifest에 없는 전체 화면 갤러리 참조: ${filename}`);
  }
}
if (galleryFiles.length !== galleryFileSet.size) {
  fail('전체 화면 갤러리에 같은 스크린샷 파일이 중복 참조됩니다.');
}

const declaredItemCount = gallery.match(/"numberOfItems"\s*:\s*(\d+)/);
if (!declaredItemCount || Number(declaredItemCount[1]) !== manifest.length) {
  fail(`갤러리 JSON-LD numberOfItems가 manifest 항목 수(${manifest.length})와 다릅니다.`);
}
if (!gallery.includes(`${manifest.length}개 화면으로 확인하세요`)) {
  fail(`갤러리 제목에 manifest 항목 수(${manifest.length})가 반영되지 않았습니다.`);
}

const home = readText(homePath);
if (!home.includes(`전체 ${manifest.length}개 화면 보기`)) {
  fail(`홈 화면 링크에 manifest 항목 수(${manifest.length})가 반영되지 않았습니다.`);
}
for (const filename of ['users.webp', 'global-search.webp']) {
  if (!home.includes(`./assets/screenshots/${filename}`)) {
    fail(`홈 화면의 필수 스크린샷 카드 누락: ${filename}`);
  }
}

const appRoutes = [
  ...readText(routesPath).matchAll(/<Route\s+path="([^"]+)"/g),
].map((match) => match[1]);
const desktopPaths = manifestPaths.filter((entry) => entry.viewport === 'desktop');

for (const route of new Set(appRoutes)) {
  if (route === '*') {
    const notFound = desktopPaths.find((entry) => entry.file === 'not-found.webp');
    if (!notFound) {
      fail('React wildcard 경로를 보여 주는 desktop not-found.webp 캡처가 없습니다.');
    } else if (appRoutes.some((knownRoute) => routeMatches(knownRoute, notFound.pathname))) {
      fail(`not-found.webp의 path가 실제 React 경로와 충돌합니다: ${notFound.path}`);
    }
    continue;
  }

  if (!desktopPaths.some((entry) => routeMatches(route, entry.pathname))) {
    fail(`핵심 React 경로의 desktop 캡처 누락: ${route}`);
  }
}

if (failures.length > 0) {
  console.error('스크린샷 검증 실패:');
  for (const failure of failures) {
    console.error(`- ${failure}`);
  }
  process.exit(1);
}

console.log(
  `스크린샷 검증 완료: manifest ${manifest.length}개, WebP ${filenames.size}개, React 경로 ${new Set(appRoutes).size}개`,
);
