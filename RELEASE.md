# Releasing moodies

This repo doubles as the agent source **and** the Homebrew tap. The formula at `Formula/moodies.rb` builds the agent from source on the user's machine.

## One-time setup

1. Create the repo on github.com (public — brew needs to fetch source):
   - Name: **`homebrew-moodies`**
   - Visibility: **public**
   - Don't init with README/license/.gitignore
2. Push from the local clone:
   ```
   git remote add origin https://github.com/shreyanshkushwaha/homebrew-moodies.git
   git push -u origin main
   ```

## Per release

1. Tag and push:
   ```
   git tag v0.1.0
   git push origin v0.1.0
   ```
   GitHub auto-generates a source tarball at:
   `https://github.com/shreyanshkushwaha/homebrew-moodies/archive/refs/tags/v0.1.0.tar.gz`

2. Compute its sha256:
   ```
   curl -sL https://github.com/shreyanshkushwaha/homebrew-moodies/archive/refs/tags/v0.1.0.tar.gz | shasum -a 256
   ```

3. Update `Formula/moodies.rb`:
   - `url` — bump tag to the new version
   - `sha256` — paste the value from step 2

4. Commit + push:
   ```
   git add Formula/moodies.rb
   git commit -m "moodies v0.1.0"
   git push
   ```

## End user install

```
brew tap shreyanshkushwaha/moodies
brew install moodies
```
