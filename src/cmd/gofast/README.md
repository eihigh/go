# gofast

`gofast` は `go build` の前段でバイナリキャッシュを利用し、同じ入力条件であれば再ビルドを高速化するための **third-party tool** です。  
Go 本体コマンドへの統合を前提にせず、単独コマンドとして利用する想定です。

## セットアップ（導入手順）

このリポジトリをローカルに clone した前提で、次の手順で `gofast` バイナリを作成できます。

```bash
cd /path/to/go/src
./make.bash
../bin/go build -o /path/to/bin/gofast cmd/gofast
```

`/path/to/bin` を `PATH` に追加すると `gofast` を直接実行できます。

## 使い方

```bash
gofast build [--explain-cache] [go build args...]
gofast watch [--explain-cache] [--debounce duration] [go build args...]
gofast clear
gofast verify-layout
```

例:

```bash
gofast build --explain-cache -o app .
gofast watch --debounce=500ms -o app .
gofast clear
```

## 補足

- キャッシュはユーザーキャッシュ配下（`<user cache dir>/gofast`）に保存されます。
- `gofast build` は安全な再利用条件を満たさない場合、自動的に通常の `go build` にフォールバックします。

## 内部動作（バイナリ構造をどう扱うか）

`gofast` はバイナリそのものを再利用しますが、無条件にコピーせず、メタデータと実体の整合性を段階的に検証します。

1. **通常ビルド時に保存する情報**
   - `go build` 実行後、出力バイナリを `entries/<entryID>/binary` に保存
   - 同時に `entries/<entryID>/meta.json` を保存し、少なくとも以下を記録
     - `binary_size`（バイト数）
     - `build_id`（`go tool buildid` で取得）
     - `env/input/stdlib/watch` の各キー
     - `goos` / `goarch` / `cgo_enabled` / `build_args`

2. **キャッシュ復元時の検証**
   - `meta.json` と `binary` が両方存在することを確認
   - `binary_size` と実ファイルサイズが一致するかを確認
   - 取得できる場合は `go tool buildid` の値と `meta.json` の `build_id` を照合
   - いずれか不一致ならそのエントリは無効とし、キャッシュ復元を中止して `go build` にフォールバック

3. **復元の実処理**
   - 検証を通過した場合のみ `binary` を出力先へコピー
   - コピーは一時ファイル経由で行い、最後に rename して反映（中途半端な出力を避ける）

4. **どのエントリを使うかの判断**
   - 直前状態（`state/*.json`）の `env/input/stdlib/watch` キーを比較して候補を選定
   - 一致条件が崩れた場合は復元せず、通常ビルド結果で新しいエントリを作り直す

この設計により、`gofast` は「キー一致」だけでなく「バイナリ実体の整合性（サイズ・BuildID）」まで確認してから再利用します。
