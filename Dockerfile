FROM 100.125.0.198:20202/hwcse/as-go:1.8.5

COPY ./testGo /home
COPY ./conf /home/conf
RUN chmod +x /home/testGo

CMD ["/home/testGo"]