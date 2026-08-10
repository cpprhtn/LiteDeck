import {defineConfig} from 'vite'
import react from '@vitejs/plugin-react'
import {writeFileSync} from 'node:fs'
import {resolve} from 'node:path'

// dist/ is build output and stays out of git, but the directory itself has to
// exist in a fresh clone: main.go does `//go:embed all:frontend/dist`, and an
// absent directory makes `go vet`, `go build` and `go test ./...` fail before
// doing any work. A committed .gitkeep satisfies the pattern — except vite
// empties outDir on every build and takes the placeholder with it, which would
// leave the repo one `git commit -a` away from breaking again for everyone.
//
// So the build puts it back. The invariant is maintained by the tool that
// breaks it rather than by asking people to remember.
function keepDistTracked() {
  return {
    name: 'litedeck-keep-dist-tracked',
    closeBundle() {
      writeFileSync(resolve(__dirname, 'dist/.gitkeep'), '')
    },
  }
}

// https://vitejs.dev/config/
export default defineConfig({
  plugins: [react(), keepDistTracked()]
})
