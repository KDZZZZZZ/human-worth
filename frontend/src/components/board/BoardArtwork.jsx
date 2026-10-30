export const threadPath = "M 481.718 280.542 C 466.050 321.892 388.005 349.869 317 353 C 245 351 193 392 192 463 C 189 554 271 642 229 705 C 202 746 186 772 185 822 C 180 894 224 918 228 971 C 233 1010 199 1053 199 1076 C 193 1134 268 1226 195 1333";

export default function BoardArtwork() {
  return <>
    <svg className="art" viewBox="0 0 763 540" aria-hidden="true">
      <g fill="none" stroke="#191a18" strokeWidth="1.05">{Array.from({ length: 10 }, (_, index) => {
        const x = 414 + index * 7.8;
        return <path key={index} d={`M 305 353 C 390 353 ${x + 7} 315 ${x} 260 C ${x - 16} 173 ${x - 11} ${75 + index * 9.6} ${518 - index * .9} ${75 + index * 9.6} C ${573 - index * 4.9} ${75 + index * 9.6} ${603 - index * 8.7} ${117 + index * 7.5} ${603 - index * 8.7} 186 L ${603 - index * 8.7} 218 C ${603 - index * 8.7} ${285 + index * 10} ${652 + index * 4} ${239 + index * 11} ${680 + index * 4} ${206 + index * 11} C ${714 + index * 4} ${169 + index * 12} 727 ${166 + index * 13.4} 763 ${168 + index * 13.4}`} />;
      })}</g>
      <circle cx="652" cy="138" r="37" fill="#ae5533" /><circle cx="625" cy="227" r="12.5" fill="#21211f" />
    </svg>
    <svg className="thread" viewBox="0 0 763 1404" preserveAspectRatio="none" aria-hidden="true">
      <path d={threadPath} fill="none" stroke="#b44822" strokeWidth="1.6" />
      <g fill="#fcf9f1" stroke="#b44822" strokeWidth="2.6">{[[192, 463], [193, 767], [199, 1076]].map(([x, y], index) => <circle key={index} cx={x} cy={y} r="9" />)}</g>
      <circle cx="195" cy="1333" r="6.3" fill="#ac4825" />
    </svg>
  </>;
}
