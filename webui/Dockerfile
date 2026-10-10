FROM node:20-alpine AS builder

WORKDIR /app

COPY package.json package-lock.json* ./

# npm ci ставит ровно то, что записано в package-lock.json — сборка
# воспроизводима и не подтягивает свежие версии.
#
# Флаг --silent здесь вреден: он скрывает и причину сбоя, так что при
# ошибке в журнале остаётся только «did not complete successfully» без
# единой подсказки (так и случилось при установке на Ubuntu). --no-audit
# и --no-fund убирают посторонний вывод, но не прячут ошибки.
RUN npm ci --no-audit --no-fund

COPY . .

RUN npm run build

FROM nginx:alpine

COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]