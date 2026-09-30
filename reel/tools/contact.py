"""Contact sheet: python3 tools/contact.py frames/ out.png [cols] [label_every_s]"""
import sys, os, glob
from PIL import Image, ImageDraw, ImageFont
d, outp = sys.argv[1], sys.argv[2]
cols = int(sys.argv[3]) if len(sys.argv) > 3 else 4
files = sorted(glob.glob(os.path.join(d, 'f*.png')))
ims = [Image.open(f) for f in files]
w, h = ims[0].size
tw, th = (w, h) if w <= 960 else (w // 2, h // 2)
rows = (len(ims) + cols - 1) // cols
sheet = Image.new('RGB', (cols * tw + (cols + 1) * 8, rows * (th + 34) + 8), (40, 40, 40))
dr = ImageDraw.Draw(sheet)
for i, (f, im) in enumerate(zip(files, ims)):
    r, c = divmod(i, cols)
    x, y = 8 + c * (tw + 8), 8 + r * (th + 34)
    sheet.paste(im.convert('RGB').resize((tw, th), Image.LANCZOS), (x, y + 26))
    fr = int(os.path.basename(f)[1:5])
    dr.text((x, y + 4), f'f{fr}  t={fr/60:.3f}s', fill=(230, 230, 230))
sheet.save(outp)
print('saved', outp, sheet.size)
