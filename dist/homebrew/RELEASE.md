# Releasing moodies via Homebrew tap

## One-time setup

1. Create the `moodies` repo on github.com (public — brew needs to fetch source).
2. Create the `homebrew-moodies` repo on github.com (public).
3. Push `doomsday-agent` (this repo) to `moodies`:
   ```
   git remote add origin https://github.com/shreyanshkushwaha/moodies.git
   git push -u origin main
   ```
4. Clone the tap repo locally:
   ```
   git clone https://github.com/shreyanshkushwaha/homebrew-moodies.git
   mkdir homebrew-moodies/Formula
   ```

## Per release

1. Tag and push:
   ```
   git tag v0.1.0
   git push origin v0.1.0
   ```
   GitHub auto-generates a source tarball at:
   `https://github.com/shreyanshkushwaha/moodies/archive/refs/tags/v0.1.0.tar.gz`

2. Compute its sha256:
   ```
   curl -sL https://github.com/shreyanshkushwaha/moodies/archive/refs/tags/v0.1.0.tar.gz | shasum -a 256
   ```

3. Copy `dist/homebrew/moodies.rb` to the tap repo at `Formula/moodies.rb`, then update:
   - `url` — set tag to the new version
   - `sha256` — paste the value from step 2

4. Commit + push the tap:
   ```
   cd ../homebrew-moodies
   cp ../moodies/dist/homebrew/moodies.rb Formula/moodies.rb
   git add Formula/moodies.rb
   git commit -m "moodies v0.1.0"
   git push
   ```

## End user install

```
brew tap shreyanshkushwaha/moodies
brew install moodies
```
