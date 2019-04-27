FROM mysql:5.7

COPY  ./*.sql /docker-entrypoint-initdb.d/

CMD ["--character-set-server=utf8mb4"]