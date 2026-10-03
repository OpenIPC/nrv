/**
 * Флаги языков.
 *
 * Нарисованы вектором прямо в коде, а не взяты символами-эмодзи. Причина
 * простая: эмодзи рисует шрифт системы, а на целевой машине шрифтов может
 * не быть вовсе — мы это уже проходили с иероглифами, когда весь китайский
 * текст показался пустыми прямоугольниками. Вектор рисуется всегда и
 * одинаково у всех.
 *
 * Флаги — государственные символы и находятся в общественном достоянии,
 * поэтому их воспроизведение здесь ничем не ограничено.
 *
 * Изображения упрощены: мелкие детали на размере в двадцать точек всё равно
 * не различить, а лишние линии делают картинку грязной.
 */

/** Общие размеры: у большинства государственных флагов отношение 2:3. */
const W = 21
const H = 14

interface FlagProps {
  /** Ширина в точках. Высота считается из отношения сторон. */
  size?: number
}

/** Общая обёртка: держит пропорции и скругление. */
function Frame({ size = W, children }: FlagProps & { children: React.ReactNode }) {
  const height = Math.round((size * H) / W)
  return (
    <svg
      width={size}
      height={height}
      viewBox={`0 0 ${W} ${H}`}
      // Скругление и тонкая рамка: светлые полосы на светлом фоне иначе
      // сливаются с подложкой, и флаг выглядит обрезанным.
      style={{ borderRadius: 2, flexShrink: 0, display: 'block' }}
      aria-hidden="true"
    >
      <rect x={0} y={0} width={W} height={H} fill="none" stroke="rgba(0,0,0,0.18)" strokeWidth={0.6} />
      {children}
    </svg>
  )
}

/** Россия: белая, синяя и красная полосы равной высоты. */
export function FlagRU({ size }: FlagProps) {
  const band = H / 3
  return (
    <Frame size={size}>
      <rect x={0} y={0} width={W} height={band} fill="#fff" />
      <rect x={0} y={band} width={W} height={band} fill="#0039a6" />
      <rect x={0} y={band * 2} width={W} height={band} fill="#d52b1e" />
    </Frame>
  )
}

/** США: тринадцать полос и синее крыло со звёздами. */
export function FlagUS({ size }: FlagProps) {
  const stripes = 13
  const band = H / stripes
  const cantonW = W * 0.4
  const cantonH = band * 7
  return (
    <Frame size={size}>
      <rect x={0} y={0} width={W} height={H} fill="#fff" />
      {Array.from({ length: stripes }, (_, i) =>
        i % 2 === 1 ? (
          <rect key={i} x={0} y={i * band} width={W} height={band} fill="#b22234" />
        ) : null,
      )}
      <rect x={0} y={0} width={cantonW} height={cantonH} fill="#3c3b6e" />
      {/* Звёзды показаны точками: пятьдесят пятиконечных звёзд на таком
          размере превратились бы в грязное пятно. */}
      {Array.from({ length: 9 }, (_, i) => (
        <circle
          key={i}
          cx={cantonW * (0.2 + 0.2 * (i % 3))}
          cy={cantonH * (0.18 + 0.3 * Math.floor(i / 3))}
          r={0.45}
          fill="#fff"
        />
      ))}
    </Frame>
  )
}

/** Китай: красное полотно и пять звёзд. */
export function FlagCN({ size }: FlagProps) {
  const star = (cx: number, cy: number, r: number) => {
    const pts = []
    for (let i = 0; i < 10; i++) {
      const radius = i % 2 === 0 ? r : r * 0.42
      const angle = (Math.PI / 5) * i - Math.PI / 2
      pts.push(`${cx + radius * Math.cos(angle)},${cy + radius * Math.sin(angle)}`)
    }
    return pts.join(' ')
  }
  return (
    <Frame size={size}>
      <rect x={0} y={0} width={W} height={H} fill="#de2910" />
      <polygon points={star(3.5, 3.5, 3)} fill="#ffde00" />
      {[
        [6.8, 1.4],
        [8.0, 2.6],
        [8.0, 4.4],
        [6.8, 5.6],
      ].map(([cx, cy], i) => (
        <polygon key={i} points={star(cx, cy, 1.2)} fill="#ffde00" />
      ))}
    </Frame>
  )
}

/**
 * КНДР: синие полосы сверху и снизу, красная середина и белый круг
 * с красной звездой.
 *
 * Пропорции флага КНДР — 1:2, но здесь оставлено общее для всех четырёх
 * флагов отношение 2:3: в списке разная ширина читается как ошибка вёрстки,
 * а не как особенность одного флага.
 *
 * Сам перевод пока сделан южной нормой. Северокорейская норма — это
 * отдельный язык со своими правилами и своим кодом (ko-KP), и одним
 * проходом по словарю его не сделать: там другая лексика, другие
 * обращения и другие написания. Расхождение описано в README и в
 * docs/TRANSLATIONS.md — чтобы человек, увидевший флаг КНДР и южное
 * написание, не счёл это ошибкой.
 */
export function FlagKP({ size }: FlagProps) {
  // Пропорции равны официальным: синяя полоса 1/6 высоты, белая
  // разделительная — узкая, остальное красное поле.
  const blue = H * 0.18
  const line = H * 0.025
  const red = H - 2 * blue - 2 * line
  const redY = blue + line
  const r = H * 0.3
  const cx = W * 0.36
  const cy = H / 2

  // Звезда: десять вершин по кругу — пять наружных и пять внутренних.
  const star = () => {
    const pts: string[] = []
    for (let i = 0; i < 10; i++) {
      const rad = i % 2 === 0 ? r * 0.62 : r * 0.26
      const angle = (Math.PI / 5) * i - Math.PI / 2
      pts.push(`${cx + rad * Math.cos(angle)},${cy + rad * Math.sin(angle)}`)
    }
    return pts.join(' ')
  }

  return (
    <Frame size={size}>
      <rect x={0} y={0} width={W} height={H} fill="#024fa2" />
      <rect x={0} y={redY} width={W} height={red} fill="#ed1c27" />
      {/* Белые разделительные полосы узкие, но без них флаг превращается
          в две полосы и перестаёт узнаваться. */}
      <rect x={0} y={blue} width={W} height={line} fill="#fff" />
      <rect x={0} y={blue + line + red} width={W} height={line} fill="#fff" />
      <circle cx={cx} cy={cy} r={r} fill="#fff" />
      <polygon points={star()} fill="#ed1c27" />
    </Frame>
  )
}

/** Флаг по коду языка. Неизвестный код — пустая рамка. */
export function Flag({ code, size }: FlagProps & { code: string }) {
  switch (code) {
    case 'ru':
      return <FlagRU size={size} />
    case 'en':
      return <FlagUS size={size} />
    case 'zh-CN':
      return <FlagCN size={size} />
    case 'ko':
      return <FlagKP size={size} />
    default:
      return null
  }
}
