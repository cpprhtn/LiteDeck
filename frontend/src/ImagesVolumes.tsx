import { useCallback, useEffect, useRef, useState } from 'react'
import {
  ListImages,
  ListVolumes,
  PruneImages,
  RemoveImage,
  RemoveVolume,
  type ActionResult,
  type Image,
  type Volume,
} from './ipc'
import { t } from './i18n'

// Images and volumes (v1.x). People open this to reclaim disk, so the two
// things it must answer are "what is big" and "what is safe to delete".

function fmtBytes(n: number): string {
  // Decimal units, matching what docker itself prints — showing 8.4 MiB next to
  // docker's own "8.82MB" for the same image reads as a discrepancy.
  if (n >= 1e9) return `${(n / 1e9).toFixed(2)} GB`
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)} MB`
  if (n >= 1e3) return `${(n / 1e3).toFixed(0)} kB`
  return `${n} B`
}

type Confirm =
  | { kind: 'image'; image: Image }
  | { kind: 'volume'; volume: Volume }
  | { kind: 'prune'; count: number; bytes: number }

export function ImagesVolumes({
  hostID,
  visible,
  onError,
}: {
  hostID: string
  visible: boolean
  onError: (msg: string) => void
}) {
  const [images, setImages] = useState<Image[]>([])
  const [volumes, setVolumes] = useState<Volume[]>([])
  const [loading, setLoading] = useState(true)
  const [pending, setPending] = useState<string | null>(null)
  const [confirm, setConfirm] = useState<Confirm | null>(null)
  const [needsRoot, setNeedsRoot] = useState<{ retry: () => void; message: string } | null>(
    null,
  )
  const inFlight = useRef(false)

  const refresh = useCallback(async () => {
    if (inFlight.current) return
    inFlight.current = true
    try {
      const [i, v] = await Promise.all([ListImages(hostID), ListVolumes(hostID)])
      setImages(i ?? [])
      setVolumes(v ?? [])
    } catch (e) {
      onError(String(e))
    } finally {
      inFlight.current = false
      setLoading(false)
    }
  }, [hostID, onError])

  useEffect(() => {
    if (!visible) return
    void refresh()
  }, [visible, refresh])

  const run = async (key: string, fn: (elevate: boolean) => Promise<ActionResult>) => {
    setPending(key)
    setNeedsRoot(null)
    setConfirm(null)
    try {
      const res = await fn(false)
      if (!res.ok) {
        if (res.needsElevation) {
          setNeedsRoot({
            message: res.error ?? t('권한이 필요합니다'),
            retry: () => void fn(true).then(() => refresh()),
          })
        } else {
          onError(res.error ?? t('실패했습니다'))
        }
        return
      }
      await refresh()
    } catch (e) {
      onError(String(e))
    } finally {
      setPending(null)
    }
  }

  const dangling = images.filter((i) => i.dangling)
  const danglingBytes = dangling.reduce((a, i) => a + i.sizeBytes, 0)
  const unusedVolumes = volumes.filter((v) => !v.inUse)
  const totalBytes = images.reduce((a, i) => a + i.sizeBytes, 0)

  if (loading) return <div className="placeholder">{t('이미지·볼륨을 읽는 중…')}</div>

  return (
    <div className="view net-view">
      <div className="view-toolbar">
        <span className="muted small">
          {t('이미지 {images}개 · 합계 {size} · 볼륨 {volumes}개', {
            images: images.length,
            size: fmtBytes(totalBytes),
            volumes: volumes.length,
          })}
        </span>
        <span className="spacer" />
        {dangling.length > 0 && (
          <button
            className="danger"
            disabled={pending !== null}
            onClick={() =>
              setConfirm({ kind: 'prune', count: dangling.length, bytes: danglingBytes })
            }
          >
            {t('미사용 레이어 정리 ({size})', { size: fmtBytes(danglingBytes) })}
          </button>
        )}
        <button className="ghost" onClick={() => void refresh()}>
          {t('새로고침')}
        </button>
      </div>

      {needsRoot && (
        <div className="elevate">
          <span>{needsRoot.message}</span>
          <button
            className="primary small-btn"
            onClick={() => {
              needsRoot.retry()
              setNeedsRoot(null)
            }}
          >
            {t('관리자 권한으로 재시도')}
          </button>
          <button className="ghost small-btn" onClick={() => setNeedsRoot(null)}>
            {t('취소')}
          </button>
        </div>
      )}

      <div className="net-body">
        <section>
          <h3 className="net-heading">
            {t('이미지')}
            <span className="muted small">
              {' '}
              {t('— 큰 순서. 사용 중이면 데몬이 삭제를 거부합니다')}
            </span>
          </h3>
          {images.length === 0 && <div className="placeholder small">{t('이미지가 없습니다.')}</div>}
          {images.length > 0 && (
            <div className="table net-table">
              <div className="thead" style={{ gridTemplateColumns: '1fr 120px 100px 90px 80px' }}>
                <div>REPOSITORY:TAG</div>
                <div className="num">SIZE</div>
                <div className="num">{t('사용 중')}</div>
                <div>{t('상태')}</div>
                <div />
              </div>
              {images.map((img) => (
                <div
                  key={img.id}
                  className="trow net-row"
                  style={{ gridTemplateColumns: '1fr 120px 100px 90px 80px' }}
                >
                  <div className="ellipsis mono" title={img.id}>
                    {img.dangling ? (
                      <span className="muted">&lt;none&gt;</span>
                    ) : (
                      `${img.repository}:${img.tag}`
                    )}
                  </div>
                  <div className="num mono">{fmtBytes(img.sizeBytes)}</div>
                  <div className="num mono">
                    {img.containers < 0 ? <span className="muted">?</span> : img.containers}
                  </div>
                  <div>
                    {img.dangling ? (
                      <span className="badge warn">{t('미사용 레이어')}</span>
                    ) : img.containers === 0 ? (
                      <span className="muted small">{t('참조 없음')}</span>
                    ) : (
                      <span className="muted small">{t('사용 중')}</span>
                    )}
                  </div>
                  <div>
                    <button
                      className="ghost small-btn"
                      disabled={pending === img.id}
                      onClick={() => setConfirm({ kind: 'image', image: img })}
                    >
                      {t('삭제')}
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </section>

        <section>
          <h3 className="net-heading">
            {t('볼륨')}
            <span className="muted small">
              {' '}
              {t(
                '— 사용 중인 볼륨은 데몬이 삭제를 거부합니다. LiteDeck은 그것을 우회하는 방법을 제공하지 않습니다',
              )}
            </span>
          </h3>
          {volumes.length === 0 && <div className="placeholder small">{t('볼륨이 없습니다.')}</div>}
          {volumes.length > 0 && (
            <div className="table net-table">
              <div className="thead" style={{ gridTemplateColumns: '1fr 90px 2fr 80px' }}>
                <div>NAME</div>
                <div>DRIVER</div>
                <div>MOUNTPOINT</div>
                <div />
              </div>
              {volumes.map((v) => (
                <div
                  key={v.name}
                  className="trow net-row"
                  style={{ gridTemplateColumns: '1fr 90px 2fr 80px' }}
                >
                  <div className="ellipsis mono">
                    {v.name}
                    {!v.inUse && <span className="badge warn">{t('미사용')}</span>}
                  </div>
                  <div className="muted mono">{v.driver}</div>
                  <div className="ellipsis muted mono" title={v.mountpoint}>
                    {v.mountpoint}
                  </div>
                  <div>
                    <button
                      className="ghost small-btn"
                      disabled={pending === v.name || v.inUse}
                      title={v.inUse ? t('사용 중인 볼륨은 삭제할 수 없습니다') : undefined}
                      onClick={() => setConfirm({ kind: 'volume', volume: v })}
                    >
                      {t('삭제')}
                    </button>
                  </div>
                </div>
              ))}
            </div>
          )}
          {unusedVolumes.length > 0 && (
            <p className="muted small">
              {t('미사용 볼륨 {n}개 — 컨테이너가 지워진 뒤에도 데이터는 남습니다. 필요 없는지 확인하고 지우세요.', { n: unusedVolumes.length })}
            </p>
          )}
        </section>
      </div>

      {confirm && (
        <div className="scrim">
          <div className="dialog">
            {confirm.kind === 'image' && (
              <>
                <h2>{t('이미지를 삭제하시겠습니까?')}</h2>
                <dl className="keyinfo">
                  <dt>{t('이미지')}</dt>
                  <dd className="mono">
                    {confirm.image.dangling
                      ? '<none>'
                      : `${confirm.image.repository}:${confirm.image.tag}`}
                  </dd>
                  <dt>{t('크기')}</dt>
                  <dd className="mono">{fmtBytes(confirm.image.sizeBytes)}</dd>
                  <dt>ID</dt>
                  <dd className="mono selectable">{confirm.image.id}</dd>
                </dl>
                {confirm.image.containers > 0 && (
                  <p className="warn-text">
                    {t('컨테이너 {n}개가 이 이미지를 쓰고 있어 데몬이 거부할 것입니다.', { n: confirm.image.containers })}
                  </p>
                )}
                <div className="dialog-actions">
                  <button onClick={() => setConfirm(null)}>{t('취소')}</button>
                  <button
                    className="danger"
                    onClick={() =>
                      void run(confirm.image.id, (e) =>
                        RemoveImage(hostID, confirm.image.id, e),
                      )
                    }
                  >
                    {t('삭제')}
                  </button>
                </div>
              </>
            )}

            {confirm.kind === 'volume' && (
              <>
                <h2>{t('볼륨을 삭제하시겠습니까?')}</h2>
                <p className="muted">
                  {t('볼륨 안의 데이터가 영구히 사라집니다. 컨테이너와 달리 다시 만들 수 없습니다.')}
                </p>
                <dl className="keyinfo">
                  <dt>{t('이름')}</dt>
                  <dd className="mono">{confirm.volume.name}</dd>
                  <dt>{t('경로')}</dt>
                  <dd className="mono selectable">{confirm.volume.mountpoint}</dd>
                </dl>
                <div className="dialog-actions">
                  <button onClick={() => setConfirm(null)}>{t('취소')}</button>
                  <button
                    className="danger"
                    onClick={() =>
                      void run(confirm.volume.name, (e) =>
                        RemoveVolume(hostID, confirm.volume.name, e),
                      )
                    }
                  >
                    {t('삭제')}
                  </button>
                </div>
              </>
            )}

            {confirm.kind === 'prune' && (
              <>
                <h2>{t('미사용 레이어를 정리하시겠습니까?')}</h2>
                <p className="muted">
                  {t('태그가 없는 레이어 {n}개, 약 {size}가 삭제됩니다. 태그가 붙은 이미지는 건드리지 않습니다.', { n: confirm.count, size: fmtBytes(confirm.bytes) })}
                </p>
                <div className="dialog-actions">
                  <button onClick={() => setConfirm(null)}>{t('취소')}</button>
                  <button
                    className="danger"
                    onClick={() => void run('prune', (e) => PruneImages(hostID, e))}
                  >
                    {t('정리')}
                  </button>
                </div>
              </>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
